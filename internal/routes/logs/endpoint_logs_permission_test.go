package logs

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	utilmocks "github.com/neutree-ai/neutree/internal/util/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestEndpointLogRoutes_RequiresReadPermission(test *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "log-sources"},
		{method: http.MethodGet, path: "logs/pod-123/logs?container=model-downloader&previous=true&download=true"},
		{method: http.MethodHead, path: "logs/pod-123/logs?download=true"},
	}
	cases := []struct {
		name          string
		userID        string
		permissionErr error
		statusCode    int
	}{
		{name: "no identity", statusCode: http.StatusUnauthorized},
		{name: "permission denied", userID: "user-123", statusCode: http.StatusForbidden},
		{
			name: "permission lookup failed", userID: "user-123", permissionErr: errors.New("permission backend unavailable"),
			statusCode: http.StatusInternalServerError,
		},
	}
	for _, workspace := range []string{"default", "other-workspace"} {
		for _, route := range routes {
			for _, testCase := range cases {
				test.Run(workspace+"/"+route.method+"/"+route.path+"/"+testCase.name, func(test *testing.T) {
					gin.SetMode(gin.TestMode)
					store := storagemocks.NewMockStorage(test)
					if testCase.userID != "" {
						store.On("CallDatabaseFunction", "has_permission", map[string]interface{}{
							"user_uuid": testCase.userID, "required_permission": "endpoint:read", "workspace": workspace,
						}, mock.Anything).Return(testCase.permissionErr).Once()
					}

					kubernetesClient := utilmocks.NewMockK8sClient(test)
					httpClient := utilmocks.NewMockHTTPClient(test)
					identity := func(context *gin.Context) { context.Set("user_id", testCase.userID) }
					router := gin.New()
					RegisterEndpointLogsRoutes(router.Group("/api/v1"), []gin.HandlerFunc{identity}, &Dependencies{
						Storage: store, K8sClient: kubernetesClient, HTTPClient: httpClient,
					})
					recorder := httptest.NewRecorder()
					request := httptest.NewRequest(route.method, "/api/v1/endpoints/"+workspace+"/test-endpoint/"+route.path, nil)
					router.ServeHTTP(recorder, request)

					assert.Equal(test, testCase.statusCode, recorder.Code, recorder.Body.String())
					store.AssertNotCalled(test, "ListEndpoint", mock.Anything)
					store.AssertNotCalled(test, "ListCluster", mock.Anything)
					assert.Empty(test, kubernetesClient.Calls)
					assert.Empty(test, httpClient.Calls)
				})
			}
		}
	}
}
