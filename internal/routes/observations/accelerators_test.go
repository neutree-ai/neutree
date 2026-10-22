package observations

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestSamplesPreserveZeroAndScrapeTime(t *testing.T) {
	now := time.Now()
	body := fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[
 {"metric":{"__name__":"neutree_accelerator_memory_used_bytes","accelerator_uuid":"gpu","node":"node","instance":"private-target"},"values":[[%d,"40"],[%d,"0"]]},
 {"metric":{"accelerator_uuid":"old"},"values":[[%d,"42"]]},
 {"metric":{"accelerator_uuid":"invalid"},"values":[[%d,"NaN"]]}
 ]}}`, now.Unix()-30, now.Unix()-15, now.Unix()-100, now.Unix())
	samples, err := decodeSamples([]byte(body), now)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	require.Zero(t, samples[0].Value)
	require.Equal(t, float64(now.Unix()-15), samples[0].ObservedAt)
	require.NotContains(t, samples[0].Labels, "instance")
}

func TestDecodeKVCacheAndNodeL1Samples(t *testing.T) {
	now := time.Now()
	body := fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[
 {"metric":{"__name__":"vllm:gpu_cache_usage_perc","application":"chat","replica":"chat-pod-0"},"values":[[%d,"0.42"]]},
 {"metric":{"__name__":"lmcache_mp_l1_memory_usage_bytes","node":"node-a","job":"neutree-zcache","instance":"runtime","pod":"cache-pod"},"values":[[%d,"2147483648"]]},
 {"metric":{"__name__":"up","job":"neutree-zcache","instance":"runtime"},"values":[[%d,"1"]]}
 ]}}`, now.Unix()-10, now.Unix()-12, now.Unix()-12)
	samples, err := decodeSamples([]byte(body), now)
	require.NoError(t, err)
	require.Len(t, samples, 2)
	require.Equal(t, "vllm:kv_cache_usage_perc", samples[0].Metric)
	require.Equal(t, "chat", samples[0].Labels["endpoint"])
	require.Equal(t, "chat-pod-0", samples[0].Labels["replica"])
	require.InDelta(t, 0.42, samples[0].Value, 1e-6)
	require.Equal(t, "lmcache_mp_l1_memory_usage_bytes", samples[1].Metric)
	require.Equal(t, "node-a", samples[1].Labels["node"])
	require.InDelta(t, 2147483648.0, samples[1].Value, 1e-3)
}

func TestRejectPartialOrMalformedResponse(t *testing.T) {
	for _, body := range []string{`{}`, `{"status":"success","isPartial":true,"data":{"resultType":"matrix"}}`, `{"status":"success","data":{"resultType":"vector"}}`} {
		_, err := decodeSamples([]byte(body), time.Now())
		require.Error(t, err)
	}
}

func TestL1SamplesRequireHealthyScrapeAndKeepOriginalTime(t *testing.T) {
	now := time.Now()
	for _, state := range []string{"0", "1"} {
		body := fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[
 {"metric":{"__name__":"lmcache_mp_l1_memory_usage_bytes","node":"node-a","pod":"old","job":"neutree-zcache","instance":"old"},"values":[[%d,"6442450944"]]},
 {"metric":{"__name__":"up","job":"neutree-zcache","instance":"old"},"values":[[%d,"%s"]]},
 {"metric":{"__name__":"lmcache_mp_l1_memory_usage_bytes","node":"node-a","pod":"new","job":"neutree-zcache","instance":"new"},"values":[[%d,"0"]]},
 {"metric":{"__name__":"up","job":"neutree-zcache","instance":"new"},"values":[[%d,"1"]]}
 ]}}`, now.Unix()-40, now.Unix()-10, state, now.Unix()-5, now.Unix()-5)
		samples, err := decodeSamples([]byte(body), now)
		require.NoError(t, err)
		want := 1
		if state == "1" {
			want = 2
		}
		require.Len(t, samples, want)
		latest := samples[len(samples)-1]
		require.Zero(t, latest.Value)
		require.Equal(t, "new", latest.Labels["pod"])
		require.Equal(t, float64(now.Unix()-5), latest.ObservedAt)
		require.NotContains(t, latest.Labels, "instance")
	}
}

func permission(t *testing.T, s *mocks.MockStorage, allowed bool) {
	s.On("CallDatabaseFunction", "has_permission", mock.Anything, mock.Anything).Run(func(args mock.Arguments) { *args.Get(2).(*bool) = allowed }).Return(nil)
}

func router(deps *Dependencies, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	middlewares := []gin.HandlerFunc{}
	if authenticated {
		middlewares = append(middlewares, func(c *gin.Context) { c.Set("user_id", "user"); c.Next() })
	}
	RegisterRoutes(r.Group(""), middlewares, deps)
	return r
}

func TestPermissionPrecedesStorageAndMonitoring(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		t.Run(fmt.Sprint(authenticated), func(t *testing.T) {
			s := mocks.NewMockStorage(t)
			if authenticated {
				permission(t, s, false)
			}
			r := router(&Dependencies{Storage: s, MetricsURL: "http://must-not-call.invalid"}, authenticated)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/clusters/other/private/accelerator-observations", nil))
			expected := http.StatusUnauthorized
			if authenticated {
				expected = http.StatusForbidden
			}
			require.Equal(t, expected, w.Code)
		})
	}
}

func TestEndpointQueriesOnlyItsWorkspaceClusterAndName(t *testing.T) {
	s := mocks.NewMockStorage(t)
	permission(t, s, true)
	s.On("ListEndpoint", resourceFilter("tenant", "model")).Return([]v1.Endpoint{{Spec: &v1.EndpointSpec{Cluster: `cluster"x`}}}, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		require.Contains(t, q, `workspace="tenant"`)
		require.Contains(t, q, `neutree_cluster="cluster\"x"`)
		require.Contains(t, q, `endpoint="model"`)
		require.Contains(t, q, "[90s]")
		require.NotContains(t, q, "neutree_accelerator_memory_used_bytes")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	}))
	defer upstream.Close()
	w := httptest.NewRecorder()
	router(&Dependencies{Storage: s, MetricsURL: upstream.URL}, true).ServeHTTP(w, httptest.NewRequest("GET", "/endpoints/tenant/model/accelerator-observations?query=malicious", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	var response Snapshot
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, "no_data", response.Status)
}

func TestClusterReadDoesNotImplyEndpointRead(t *testing.T) {
	s := mocks.NewMockStorage(t)
	s.On("CallDatabaseFunction", "has_permission", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		*args.Get(2).(*bool) = args.Get(1).(map[string]interface{})["required_permission"] == "cluster:read"
	}).Return(nil)
	s.On("ListCluster", resourceFilter("tenant", "cluster")).Return([]v1.Cluster{{}}, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotContains(t, r.URL.Query().Get("query"), "neutree_endpoint_")
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	}))
	defer upstream.Close()
	w := httptest.NewRecorder()
	router(&Dependencies{Storage: s, MetricsURL: upstream.URL}, true).ServeHTTP(w, httptest.NewRequest("GET", "/clusters/tenant/cluster/accelerator-observations", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
}

func TestEndpointOnlyReaderGetsAssignedHardware(t *testing.T) {
	s := mocks.NewMockStorage(t)
	s.On("CallDatabaseFunction", "has_permission", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		*args.Get(2).(*bool) = args.Get(1).(map[string]interface{})["required_permission"] == "endpoint:read"
	}).Return(nil)
	s.On("ListEndpoint", resourceFilter("tenant", "model")).Return([]v1.Endpoint{{Spec: &v1.EndpointSpec{Cluster: "cluster"}, Status: &v1.EndpointStatus{Resources: &v1.EndpointResourceStatus{Replicas: []v1.ReplicaDeviceAllocation{
		{NodeID: "node-a", Devices: []v1.DeviceAllocation{{UUID: "gpu.1"}, {UUID: "gpu.1"}}},
		{NodeID: "node-b", Devices: []v1.DeviceAllocation{{UUID: "gpu-2"}}},
	}}}}}, nil)
	queries := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		require.Contains(t, q, `workspace="tenant"`)
		require.Contains(t, q, `neutree_cluster="cluster"`)
		require.Contains(t, q, "[90s]")
		require.NotContains(t, q, "[90s:]")
		if len(queries) == 1 {
			require.Contains(t, q, `endpoint="model"`)
		} else {
			require.NotContains(t, q, `endpoint=`)
			require.NotContains(t, q, "neutree_accelerator_memory_used_bytes")
			require.NotContains(t, q, "neutree_accelerator_utilization_ratio")
			require.Contains(t, q, "neutree_node_accelerator_npu_info")
			require.Contains(t, q, "neutree_node_accelerator_hcu_info")
			if strings.Contains(q, `node="node-a"`) {
				require.Contains(t, q, `accelerator_uuid=~"gpu\\.1"`)
				require.NotContains(t, q, "gpu-2")
			} else {
				require.Contains(t, q, `node="node-b"`)
				require.Contains(t, q, `accelerator_uuid=~"gpu-2"`)
				require.NotContains(t, q, "gpu.1")
			}
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"neutree_node_accelerator_npu_info","node":"node-a","accelerator_uuid":"gpu.1","driver_version":"24.1","hccs_capable":"1","private":"hidden"},"values":[[%d,"1"]]}]}}`, time.Now().Unix()-20)
	}))
	defer upstream.Close()
	w := httptest.NewRecorder()
	router(&Dependencies{Storage: s, MetricsURL: upstream.URL}, true).ServeHTTP(w, httptest.NewRequest("GET", "/endpoints/tenant/model/accelerator-observations", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Len(t, queries, 3)
	require.Contains(t, w.Body.String(), `"hccs_capable":"1"`)
	require.NotContains(t, w.Body.String(), "hidden")
}
