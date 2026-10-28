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
	"github.com/neutree-ai/neutree/pkg/cacheobservations"
	"github.com/neutree-ai/neutree/pkg/kvcache"
	"github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"regexp"
	"strconv"
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

func TestRejectPartialOrMalformedResponse(t *testing.T) {
	for _, body := range []string{`{}`, `{"status":"success","isPartial":true,"data":{"resultType":"matrix"}}`, `{"status":"success","data":{"resultType":"vector"}}`} {
		_, err := decodeSamples([]byte(body), time.Now())
		require.Error(t, err)
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
			require.Contains(t, q, "neutree_node_accelerator_example_info")
			require.Contains(t, q, "neutree_node_accelerator_other_info")
			if strings.Contains(q, `node="node-a"`) {
				require.Contains(t, q, `accelerator_uuid=~"gpu\\.1"`)
				require.NotContains(t, q, "gpu-2")
			} else {
				require.Contains(t, q, `node="node-b"`)
				require.Contains(t, q, `accelerator_uuid=~"gpu-2"`)
				require.NotContains(t, q, "gpu.1")
			}
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"neutree_node_accelerator_example_info","node":"node-a","accelerator_uuid":"gpu.1","driver_version":"24.1","example_capable":"1","private":"hidden"},"values":[[%d,"1"]]}]}}`, time.Now().Unix()-20)
	}))
	defer upstream.Close()
	w := httptest.NewRecorder()
	router(&Dependencies{Storage: s, MetricsURL: upstream.URL, ExtraAcceleratorMetrics: []string{"neutree_node_accelerator_example_info", "neutree_node_accelerator_other_info"}, ExtraAcceleratorLabels: []string{"example_capable"}}, true).ServeHTTP(w, httptest.NewRequest("GET", "/endpoints/tenant/model/accelerator-observations", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Len(t, queries, 3)
	require.Contains(t, w.Body.String(), `"example_capable":"1"`)
	require.NotContains(t, w.Body.String(), "hidden")
}

func TestEndpointCacheUsesOneScopedL1Query(t *testing.T) {
	s := mocks.NewMockStorage(t)
	permission(t, s, true)
	s.On("ListEndpoint", resourceFilter("tenant", "model")).Return([]v1.Endpoint{{
		Spec: &v1.EndpointSpec{Cluster: "cluster", KVCache: &v1.EndpointKVCacheSpec{Provider: "example"}},
		Status: &v1.EndpointStatus{Resources: &v1.EndpointResourceStatus{Replicas: []v1.ReplicaDeviceAllocation{
			{NodeID: "node.a", Devices: []v1.DeviceAllocation{{UUID: "gpu-1"}, {UUID: "gpu-2"}}},
			{NodeID: "node-b", Devices: []v1.DeviceAllocation{{UUID: "gpu-3"}}},
		}}},
	}}, nil)
	var queries []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("query"))
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	}))
	defer upstream.Close()

	w := httptest.NewRecorder()
	router(&Dependencies{Storage: s, MetricsURL: upstream.URL, KVCacheProviders: kvcache.Providers{"example": resourceObserver{}}}, true).ServeHTTP(w,
		httptest.NewRequest("GET", "/endpoints/tenant/model/accelerator-observations", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	// One replica query, two physical-device queries, and one shared L1 query.
	require.Len(t, queries, 4)
	l1Queries := 0
	for _, query := range queries {
		require.NotContains(t, query, "vllm:kv_cache_usage_perc")
		require.NotContains(t, query, "vllm:gpu_cache_usage_perc")
		if strings.Contains(query, "example_cache") {
			l1Queries++
			require.Contains(t, query, `node=~"node-b|node\\.a"`)
			require.Contains(t, query, `workspace="tenant"`)
			require.Contains(t, query, `neutree_cluster="cluster"`)
			require.Contains(t, query, `example_cache`)
			require.NotContains(t, query, "accelerator_uuid")
		}
	}
	require.Equal(t, 1, l1Queries)
	var snap Snapshot
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snap))
	require.Equal(t, []Sample{{Metric: "example_cache", Value: 0, ObservedAt: 123}}, snap.Samples)
}

func TestHardwareLabelsRequireExplicitExtension(t *testing.T) {
	now := time.Now()
	body := fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"neutree_node_accelerator_example_info","node":"node","accelerator_uuid":"gpu","example_capable":"1","private":"hidden"},"values":[[%d,"1"]]}]}}`, now.Unix())
	samples, err := decodeSamples([]byte(body), now)
	require.NoError(t, err)
	require.NotContains(t, samples[0].Labels, "example_capable")
	samples, err = decodeSamples([]byte(body), now, "example_capable")
	require.NoError(t, err)
	require.Equal(t, "1", samples[0].Labels["example_capable"])
	require.NotContains(t, samples[0].Labels, "private")
}

type resourceObserver struct{ kvcache.VLLMNative }

func (resourceObserver) ResourceQueries(scope cacheobservations.Scope) []cacheobservations.Query {
	nodes := []string{}
	for _, node := range scope.Nodes {
		nodes = append(nodes, regexp.QuoteMeta(node))
	}
	return []cacheobservations.Query{{Expression: fmt.Sprintf(`example_cache{workspace=%s,neutree_cluster=%s,node=~%s}`, strconv.Quote(scope.Workspace), strconv.Quote(scope.Cluster), strconv.Quote(strings.Join(nodes, "|"))), Decode: func([]byte, time.Time) ([]cacheobservations.Sample, error) {
		return []cacheobservations.Sample{{Metric: "example_cache", Value: 0, ObservedAt: 123}}, nil
	}}}
}
