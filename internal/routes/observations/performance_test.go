package observations

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestPerformanceRequiresEndpointRead(t *testing.T) {
	s := mocks.NewMockStorage(t)
	permission(t, s, false)
	w := httptest.NewRecorder()
	router(&Dependencies{Storage: s, MetricsURL: "http://must-not-call.invalid"}, true).ServeHTTP(
		w, httptest.NewRequest("GET", "/endpoints/tenant/model/performance-observations", nil),
	)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestPerformanceWindowNormalizationAndQueryScope(t *testing.T) {
	s := mocks.NewMockStorage(t)
	permission(t, s, true)
	s.On("ListEndpoint", resourceFilter("tenant", "chat")).Return([]v1.Endpoint{{
		Spec: &v1.EndpointSpec{
			Cluster: "cluster-a",
			KVCache: &v1.EndpointKVCacheSpec{Provider: "zcache"},
		},
		Status: &v1.EndpointStatus{
			Phase: v1.EndpointPhaseRUNNING,
			Resources: &v1.EndpointResourceStatus{
				Replicas: []v1.ReplicaDeviceAllocation{
					{NodeID: "worker.1", Devices: []v1.DeviceAllocation{{UUID: "gpu-0"}}},
				},
			},
		},
	}}, nil)

	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query().Get("query")
		require.Contains(t, q, `workspace="tenant"`)
		require.Contains(t, q, `neutree_cluster="cluster-a"`)
		require.Contains(t, q, `application="chat"`)
		require.Contains(t, q, "[1h]")
		require.Contains(t, q, `node=~"^(worker\\.1)$"`)

		body := `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"metric":"ttft_p95_seconds"},"value":[1792736000,"0.185"]},
			{"metric":{"metric":"ttft_avg_seconds"},"value":[1792736000,"0.12"]},
			{"metric":{"metric":"tpot_p95_seconds"},"value":[1792736000,"0.032"]},
			{"metric":{"metric":"tpot_avg_seconds"},"value":[1792736000,"0.024"]},
			{"metric":{"metric":"prompt_tokens_per_second"},"value":[1792736000,"420.5"]},
			{"metric":{"metric":"generation_tokens_per_second"},"value":[1792736000,"85.25"]},
			{"metric":{"metric":"requests_per_second"},"value":[1792736000,"3.2"]},
			{"metric":{"metric":"l0_hit_rate"},"value":[1792736000,"0.35"]},
			{"metric":{"metric":"l1_hit_rate"},"value":[1792736000,"0.48"]},
			{"metric":{"metric":"l1_read_bytes_per_second"},"value":[1792736000,"536870912"]},
			{"metric":{"metric":"l1_write_bytes_per_second"},"value":[1792736000,"134217728"]}
		]}}`

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	w := httptest.NewRecorder()
	router(&Dependencies{Storage: s, MetricsURL: "http://vmselect", Client: client}, true).ServeHTTP(
		w, httptest.NewRequest("GET", "/endpoints/tenant/chat/performance-observations?window=1h", nil),
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var snap PerformanceSnapshot
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snap))
	require.Equal(t, "ok", snap.Status)
	require.Equal(t, "1h", snap.Window)
	require.False(t, snap.Metrics.LatencyCumulative)
	require.False(t, snap.Metrics.HitRateCumulative)
	require.InDelta(t, 0.185, *snap.Metrics.TTFTP95Seconds, 1e-6)
	require.InDelta(t, 0.12, *snap.Metrics.TTFTAvgSeconds, 1e-6)
	require.InDelta(t, 0.032, *snap.Metrics.TPOTP95Seconds, 1e-6)
	require.InDelta(t, 0.024, *snap.Metrics.TPOTAvgSeconds, 1e-6)
	require.InDelta(t, 420.5, *snap.Metrics.PromptTokensPerSecond, 1e-6)
	require.InDelta(t, 85.25, *snap.Metrics.GenerationTokensPerSecond, 1e-6)
	require.InDelta(t, 505.75, *snap.Metrics.TotalTokensPerSecond, 1e-6)
	require.InDelta(t, 0.35, *snap.Metrics.L0HitRate, 1e-6)
	require.InDelta(t, 0.48, *snap.Metrics.L1HitRate, 1e-6)
	require.InDelta(t, 536870912.0, *snap.Metrics.L1ReadBytesPerSecond, 1e-3)
	require.InDelta(t, 134217728.0, *snap.Metrics.L1WriteBytesPerSecond, 1e-3)
}

func TestPerformanceCumulativeFallbackWhenWindowIdle(t *testing.T) {
	values := map[string]float64{
		"cum_ttft_p95_seconds":         0.21,
		"cum_ttft_avg_seconds":         0.14,
		"cum_tpot_p95_seconds":         0.04,
		"cum_tpot_avg_seconds":         0.028,
		"prompt_tokens_per_second":     0,
		"generation_tokens_per_second": 0,
		"cum_l0_hit_rate":              0.25,
		"cum_l1_hit_rate":              0.62,
		"l1_read_bytes_per_second":     0,
		"l1_write_bytes_per_second":    0,
	}
	m := assemblePerformanceMetrics(values)
	require.True(t, m.LatencyCumulative)
	require.True(t, m.HitRateCumulative)
	require.InDelta(t, 0.21, *m.TTFTP95Seconds, 1e-6)
	require.InDelta(t, 0.028, *m.TPOTAvgSeconds, 1e-6)
	require.InDelta(t, 0.0, *m.TotalTokensPerSecond, 1e-6)
	require.InDelta(t, 0.25, *m.L0HitRate, 1e-6)
	require.InDelta(t, 0.62, *m.L1HitRate, 1e-6)
}
