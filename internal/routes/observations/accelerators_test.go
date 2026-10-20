package observations

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
