package proxies

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestDirectEndpointProxyPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, "/team/model/v1/models", r.URL.EscapedPath())
		assert.Empty(t, r.URL.RawQuery, "endpoint identity must not turn into a query")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	endpoint := v1.Endpoint{Metadata: &v1.Metadata{Workspace: "team", Name: "model"}, Spec: &v1.EndpointSpec{Cluster: "cluster"}}
	store := &storagemocks.MockStorage{}
	store.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)
	store.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
		Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}, Status: &v1.ClusterStatus{DashboardURL: server.URL},
	}}, nil)
	deps := &Dependencies{Storage: store}
	workspace, name := endpoint.Metadata.Workspace, endpoint.Metadata.Name
	modelsURL, err := resolveModelsURL(deps, &testConnectivityRequest{Workspace: &workspace, EndpointRef: &name})
	require.NoError(t, err)
	assert.Equal(t, server.URL+"/team/model/v1/models", modelsURL)

	router := gin.New()
	router.GET("/probe", func(context *gin.Context) {
		context.Params = gin.Params{{Key: "workspace", Value: workspace}, {Key: "name", Value: name}, {Key: "path", Value: "/v1/models"}}
		handleServeProxy(deps)(context)
	})
	proxy := httptest.NewServer(router)
	defer proxy.Close()
	response, err := proxy.Client().Get(proxy.URL + "/probe")
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, 1, requests)
	store.AssertExpectations(t)
}
