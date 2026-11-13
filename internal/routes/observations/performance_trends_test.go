package observations

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/require"
)

func TestPerformanceRange(t *testing.T) {
	for _, tc := range []struct {
		query      string
		span, step int64
		invalid    bool
	}{
		{"", 300, 15, false}, {"window=1h", 3600, 15, false}, {"window=24h", 86400, 240, false},
		{"start=100&end=2592100", 2592000, 7200, false},
		{query: "window=invalid", invalid: true}, {query: "start=100", invalid: true},
		{query: "start=200&end=100", invalid: true}, {query: "start=0&end=2592001", invalid: true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			q, err := url.ParseQuery(tc.query)
			require.NoError(t, err)
			got, err := performanceRange(q, time.Unix(3000000, 0))
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.span, got.End-got.Start)
			require.Equal(t, tc.step, got.Step)
		})
	}
}

func TestPerformanceTrendsRoute(t *testing.T) {
	s := mocks.NewMockStorage(t)
	permission(t, s, true)
	s.On("ListEndpoint", resourceFilter("tenant", "chat")).Return([]v1.Endpoint{{Spec: &v1.EndpointSpec{Cluster: "cluster-a"}}}, nil)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/api/v1/query_range", r.URL.Path)
		q := r.URL.Query()
		require.Equal(t, "100", q.Get("start"))
		require.Equal(t, "400", q.Get("end"))
		require.Equal(t, "15", q.Get("step"))
		require.Contains(t, q.Get("query"), `workspace="tenant",neutree_cluster="cluster-a",application="chat"`)
		require.Contains(t, q.Get("query"), "[60s]")
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":{"resultType":"matrix","result":[
   {"metric":{"metric":"prompt_tokens_per_second"},"values":[[100,"10"],[115,"0"]]},
   {"metric":{"metric":"generation_tokens_per_second"},"values":[[100,"2"],[115,"0"]]},
   {"metric":{"metric":"ttft_avg_seconds"},"values":[[100,"0.2"],[115,"NaN"]]},
   {"metric":{"metric":"cum_ttft_avg_seconds"},"values":[[115,"0.2"]]},
   {"metric":{"metric":"l1_hit_rate"},"values":[[100,"0.8"]]}
  ]}}`))}, nil
	})}
	w := httptest.NewRecorder()
	router(&Dependencies{Storage: s, MetricsURL: "http://metrics", Client: client}, true).ServeHTTP(w, httptest.NewRequest("GET", "/endpoints/tenant/chat/performance-trends?start=100&end=400", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	var got performanceTrends
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, "ok", got.Status)
	require.Equal(t, "custom", got.Window)
	byMetric := map[string]performanceSeries{}
	for _, series := range got.Series {
		byMetric[series.Metric] = series
	}
	require.Equal(t, []performancePoint{{Timestamp: 100, Value: 12}, {Timestamp: 115, Value: 0}}, byMetric["total_tokens_per_second"].Points)
	require.Equal(t, []performancePoint{{Timestamp: 100, Value: 0.2}}, byMetric["ttft_avg_seconds"].Points)
	require.Equal(t, "ratio", byMetric["l1_hit_rate"].Unit)
	require.NotContains(t, byMetric, "cum_ttft_avg_seconds")
}

func TestPerformanceTrendsMonitoringStates(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		invalid    bool
	}{
		{"empty", `{"status":"success","data":{"resultType":"matrix","result":[]}}`, false},
		{"partial", `{"status":"success","isPartial":true,"data":{"resultType":"matrix","result":[]}}`, true},
		{"error", `{"status":"error"}`, true},
		{"wrong type", `{"status":"success","data":{"resultType":"vector"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			series, err := decodePerformanceTrends([]byte(tc.body), performanceTrends{Start: 100, End: 400})
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, series)
			require.Empty(t, series)
		})
	}
}
