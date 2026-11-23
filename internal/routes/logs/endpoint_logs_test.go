package logs

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	"github.com/neutree-ai/neutree/internal/util"
	utilmocks "github.com/neutree-ai/neutree/internal/util/mocks"
	"github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// setupTestDeps creates test dependencies with mock clients
func setupTestDeps(mockStorage *mocks.MockStorage) *Dependencies {
	return &Dependencies{
		Storage:    mockStorage,
		HTTPClient: &util.DefaultHTTPClient{},
		K8sClient:  &util.DefaultK8sClient{},
	}
}

// createLogsMockContext creates a mock Gin context for logs endpoints testing
func createLogsMockContext(method, path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	req := httptest.NewRequest(method, path, nil)
	c.Request = req

	// Parse URL path parameters
	// Pattern: /api/v1/endpoints/:workspace/:name/...
	parts := strings.Split(strings.Trim(path, "/"), "/")

	if len(parts) >= 4 {
		workspace := parts[3] // workspace
		name := parts[4]      // name

		c.Params = []gin.Param{
			{Key: "workspace", Value: workspace},
			{Key: "name", Value: name},
		}

		// For logs endpoint: /api/v1/endpoints/:workspace/:name/logs/:replica_id/:log_type
		if len(parts) >= 7 && parts[5] == "logs" {
			c.Params = append(c.Params,
				gin.Param{Key: "replica_id", Value: parts[6]},
				gin.Param{Key: "log_type", Value: parts[7]},
			)
		}
	}

	return c, w
}

// ===== Tests for handleGetLogSources =====

func TestHandleGetLogSources_MissingWorkspace(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	deps := setupTestDeps(mockStorage)

	c, w := createLogsMockContext("GET", "/api/v1/endpoints///log-sources")
	c.Params = []gin.Param{{Key: "name", Value: "test-endpoint"}}

	handler := handleGetLogSources(deps)
	handler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "workspace and name are required", response["error"])
}

func TestHandleGetLogSources_MissingName(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	deps := setupTestDeps(mockStorage)

	c, w := createLogsMockContext("GET", "/api/v1/endpoints/default//log-sources")
	c.Params = []gin.Param{{Key: "workspace", Value: "default"}}

	handler := handleGetLogSources(deps)
	handler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "workspace and name are required", response["error"])
}

func TestHandleGetLogSources_EndpointNotFound(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	deps := setupTestDeps(mockStorage)

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)

	c, w := createLogsMockContext("GET", "/api/v1/endpoints/default/non-existent/log-sources")

	handler := handleGetLogSources(deps)
	handler(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
	mockStorage.AssertExpectations(t)
}

func TestHandleGetLogSources_StorageError(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	deps := setupTestDeps(mockStorage)

	mockError := errors.New("storage error")
	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, mockError)

	c, w := createLogsMockContext("GET", "/api/v1/endpoints/default/test-endpoint/log-sources")

	handler := handleGetLogSources(deps)
	handler(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["error"], "failed to list endpoints")

	mockStorage.AssertExpectations(t)
}

func TestHandleGetLogSources_UnsupportedClusterType(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	deps := setupTestDeps(mockStorage)

	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-endpoint",
		},
		Spec: &v1.EndpointSpec{
			Cluster: "test-cluster",
		},
	}

	cluster := v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Spec: &v1.ClusterSpec{
			Type: "UnsupportedType",
		},
	}

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)
	mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)

	c, w := createLogsMockContext("GET", "/api/v1/endpoints/default/test-endpoint/log-sources")

	handler := handleGetLogSources(deps)
	handler(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["error"], "unsupported cluster type")

	mockStorage.AssertExpectations(t)
}

// ===== Tests for handleGetLogs =====

func TestHandleGetLogs_MissingParameters(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	deps := setupTestDeps(mockStorage)

	tests := []struct {
		name     string
		path     string
		params   []gin.Param
		wantCode int
	}{
		{
			name: "missing workspace",
			path: "/api/v1/endpoints///logs/pod-123/logs",
			params: []gin.Param{
				{Key: "name", Value: "endpoint"},
				{Key: "replica_id", Value: "pod-123"},
				{Key: "log_type", Value: "logs"},
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "missing replica_id",
			path: "/api/v1/endpoints/default/endpoint/logs//logs",
			params: []gin.Param{
				{Key: "workspace", Value: "default"},
				{Key: "name", Value: "endpoint"},
				{Key: "log_type", Value: "logs"},
			},
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, w := createLogsMockContext("GET", tt.path)
			c.Params = tt.params

			handler := handleGetLogs(deps)
			handler(c)

			assert.Equal(t, tt.wantCode, w.Code)
		})
	}
}

func TestHandleGetLogs_HEAD_Request(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	deps := setupTestDeps(mockStorage)
	kubernetesClient := utilmocks.NewMockK8sClient(t)
	deps.K8sClient = kubernetesClient
	kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(endpointLogTestPod(), nil).Once()

	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-endpoint",
		},
		Spec: &v1.EndpointSpec{
			Cluster: "test-cluster",
		},
	}
	cluster := v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Spec: &v1.ClusterSpec{
			Type: v1.KubernetesClusterType,
		},
	}

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)
	mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)

	ginContext, recorder := createLogsMockContext(http.MethodHead, "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs")
	handler := handleGetLogs(deps)
	handler(ginContext)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"))
	assert.Equal(t, "chunked", recorder.Header().Get("Transfer-Encoding"))
	assert.Empty(t, recorder.Body.String())

	mockStorage.AssertExpectations(t)
}

func TestHandleGetLogs_DownloadParameter(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	deps := setupTestDeps(mockStorage)
	kubernetesClient := utilmocks.NewMockK8sClient(t)
	deps.K8sClient = kubernetesClient
	kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(endpointLogTestPod(), nil).Once()

	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-endpoint",
		},
		Spec: &v1.EndpointSpec{
			Cluster: "test-cluster",
		},
	}
	cluster := v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Spec: &v1.ClusterSpec{
			Type: v1.KubernetesClusterType,
		},
	}

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)
	mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)

	ginContext, recorder := createLogsMockContext(http.MethodHead, "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs")
	ginContext.Request.URL.RawQuery = "download=true"
	handler := handleGetLogs(deps)
	handler(ginContext)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "attachment; filename=default-test-endpoint-pod-123-serve-current.log", recorder.Header().Get("Content-Disposition"))

	mockStorage.AssertExpectations(t)
}

func TestHandleGetLogs_LinesParameter(t *testing.T) {
	tests := []struct {
		name          string
		queryParam    string
		expectedLines int64
	}{
		{
			name:          "default lines",
			queryParam:    "",
			expectedLines: 1000,
		},
		{
			name:          "custom lines",
			queryParam:    "lines=5000",
			expectedLines: 5000,
		},
		{
			name:          "invalid lines uses default",
			queryParam:    "lines=invalid",
			expectedLines: 1000,
		},
		{
			name:          "negative lines uses default",
			queryParam:    "lines=-100",
			expectedLines: 1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := new(mocks.MockStorage)
			deps := setupTestDeps(mockStorage)
			kubernetesClient := utilmocks.NewMockK8sClient(t)
			deps.K8sClient = kubernetesClient
			kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(endpointLogTestPod(), nil).Once()
			kubernetesClient.On("GetPodLogs", mock.Anything, mock.Anything, mock.Anything, "pod-123",
				mock.MatchedBy(func(options *corev1.PodLogOptions) bool {
					return options.TailLines != nil && *options.TailLines == tt.expectedLines
				})).Return(io.NopCloser(strings.NewReader("logs")), nil).Once()

			endpoint := v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "test-endpoint",
				},
				Spec: &v1.EndpointSpec{
					Cluster: "test-cluster",
				},
			}

			cluster := v1.Cluster{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "test-cluster",
				},
				Spec: &v1.ClusterSpec{
					Type: v1.KubernetesClusterType,
				},
			}

			mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)
			mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)

			c, w := createLogsMockContext("GET", "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs")
			// Set query parameter properly
			if tt.queryParam != "" {
				c.Request.URL.RawQuery = tt.queryParam
			}

			handler := handleGetLogs(deps)
			handler(c)

			assert.Equal(t, http.StatusOK, w.Code)
			mockStorage.AssertExpectations(t)
		})
	}
}

// ===== Tests for setupStreamingResponse =====

func TestSetupStreamingResponse(t *testing.T) {
	t.Run("without download", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		setupStreamingResponse(c, false, "test.log")

		assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
		assert.Equal(t, "chunked", w.Header().Get("Transfer-Encoding"))
		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
		assert.Empty(t, w.Header().Get("Content-Disposition"))
	})

	t.Run("with download", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		setupStreamingResponse(c, true, "test-endpoint.log")

		assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
		assert.Equal(t, "chunked", w.Header().Get("Transfer-Encoding"))
		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "attachment; filename=test-endpoint.log", w.Header().Get("Content-Disposition"))
	})
}

// ===== Tests for getEndpointAndCluster =====

func TestGetEndpointAndCluster(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mockStorage := new(mocks.MockStorage)
		deps := setupTestDeps(mockStorage)

		endpoint := v1.Endpoint{
			Metadata: &v1.Metadata{
				Workspace: "default",
				Name:      "test-endpoint",
			},
			Spec: &v1.EndpointSpec{
				Cluster: "test-cluster",
			},
		}

		cluster := v1.Cluster{
			Metadata: &v1.Metadata{
				Workspace: "default",
				Name:      "test-cluster",
			},
		}

		mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)
		mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)

		resultEndpoint, resultCluster, err := getEndpointAndCluster(deps, "default", "test-endpoint")

		assert.NoError(t, err)
		assert.NotNil(t, resultEndpoint)
		assert.NotNil(t, resultCluster)
		assert.Equal(t, "test-endpoint", resultEndpoint.Metadata.Name)
		assert.Equal(t, "test-cluster", resultCluster.Metadata.Name)

		mockStorage.AssertExpectations(t)
	})

	t.Run("endpoint not found", func(t *testing.T) {
		mockStorage := new(mocks.MockStorage)
		deps := setupTestDeps(mockStorage)

		mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)

		_, _, err := getEndpointAndCluster(deps, "default", "non-existent")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "endpoint not found")

		mockStorage.AssertExpectations(t)
	})

	t.Run("cluster not found", func(t *testing.T) {
		mockStorage := new(mocks.MockStorage)
		deps := setupTestDeps(mockStorage)

		endpoint := v1.Endpoint{
			Metadata: &v1.Metadata{
				Workspace: "default",
				Name:      "test-endpoint",
			},
			Spec: &v1.EndpointSpec{
				Cluster: "test-cluster",
			},
		}

		mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)
		mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{}, nil)

		_, _, err := getEndpointAndCluster(deps, "default", "test-endpoint")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cluster not found")

		mockStorage.AssertExpectations(t)
	})
}

// ===== Tests for getRayLogSources with mock HTTP client =====

func TestGetRayLogSources_Success(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard:8265",
		},
	}

	// Mock successful HTTP response with RayApplicationsResponse structure
	responseBody := `{
		"applications": {
			"default_test-endpoint": {
				"name": "default_test-endpoint",
				"deployments": {
					"deployment-1": {
						"replicas": [
							{
								"replica_id": "replica-1"
							}
						]
					}
				}
			}
		}
	}`
	mockHTTPClient.On("Get", mock.MatchedBy(func(url string) bool {
		return strings.Contains(url, "serve/applications")
	})).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(responseBody)),
		Header:     make(http.Header),
	}, nil)

	result, err := getRayLogSources(cluster, mockHTTPClient, "default", "test-endpoint")

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotEmpty(t, result.Deployments)
	assert.Equal(t, "deployment-1", result.Deployments[0].Name)
	assert.NotEmpty(t, result.Deployments[0].Replicas)
}

func TestGetRayLogSources_HTTPError(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard:8265",
		},
	}

	// Mock HTTP error
	mockHTTPClient.On("Get", mock.Anything).Return((*http.Response)(nil), errors.New("connection refused"))

	result, err := getRayLogSources(cluster, mockHTTPClient, "default", "test-endpoint")

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "connection refused")
}

func TestGetRayLogSources_InvalidJSON(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard:8265",
		},
	}

	// Mock response with invalid JSON
	mockHTTPClient.On("Get", mock.Anything).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("invalid json")),
		Header:     make(http.Header),
	}, nil)

	result, err := getRayLogSources(cluster, mockHTTPClient, "default", "test-endpoint")

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "invalid character")
}

func TestGetRayLogSources_Non200Status(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard:8265",
		},
	}

	// Mock 404 response
	mockHTTPClient.On("Get", mock.Anything).Return(&http.Response{
		StatusCode: 404,
		Body:       io.NopCloser(strings.NewReader("not found")),
		Header:     make(http.Header),
	}, nil)

	result, err := getRayLogSources(cluster, mockHTTPClient, "default", "test-endpoint")

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "Ray Dashboard API returned status 404")
}

func TestGetRayLogSources_ApplicationNotFound(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard:8265",
		},
	}

	// Mock response without the requested application
	responseBody := `{
		"applications": {
			"other_application": {
				"name": "other_application"
			}
		}
	}`
	mockHTTPClient.On("Get", mock.Anything).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(responseBody)),
		Header:     make(http.Header),
	}, nil)

	result, err := getRayLogSources(cluster, mockHTTPClient, "default", "test-endpoint")

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "application default_test-endpoint not found")
}

// ===== Tests for streamRayLogs with mock HTTP client =====

func TestStreamRayLogs_Success(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard:8265",
		},
	}

	// Mock successful applications API response
	appsResponse := `{
		"applications": {
			"default_test-endpoint": {
				"deployments": {
					"deployment-1": {
						"replicas": [
							{
								"replica_id": "replica-1",
								"node_id": "node-1",
								"actor_id": "actor-1",
								"log_file_path": "/tmp/logs/application.log"
							}
						]
					}
				}
			}
		}
	}`
	mockHTTPClient.On("Get", "http://ray-dashboard:8265/api/serve/applications/").Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(appsResponse)),
		Header:     make(http.Header),
	}, nil).Once()

	// Mock successful log stream
	logContent := "log line 1\nlog line 2\nlog line 3\n"
	mockHTTPClient.On("Get", mock.MatchedBy(func(url string) bool {
		return strings.Contains(url, "api/v0/logs/file") && strings.Contains(url, "lines=1000")
	})).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(logContent)),
		Header:     make(http.Header),
	}, nil).Once()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	err := streamRayLogs(c, cluster, mockHTTPClient, "default", "test-endpoint", "replica-1", "application", 1000)

	assert.NoError(t, err)
	assert.Equal(t, logContent, w.Body.String())
}

func TestStreamRayLogs_HTTPError(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard:8265",
		},
	}

	// Mock HTTP error
	mockHTTPClient.On("Get", mock.Anything).Return((*http.Response)(nil), errors.New("timeout"))

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	err := streamRayLogs(c, cluster, mockHTTPClient, "default", "test-endpoint", "replica-1", "application", 1000)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestStreamRayLogs_Non200Status(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard:8265",
		},
	}

	// Mock 500 response
	mockHTTPClient.On("Get", mock.Anything).Return(&http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(strings.NewReader("internal error")),
		Header:     make(http.Header),
	}, nil)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	err := streamRayLogs(c, cluster, mockHTTPClient, "default", "test-endpoint", "replica-1", "application", 1000)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Ray Dashboard API returned status 500")
}

// ===== Tests for streamK8sLogs with mock K8s client =====

func TestStreamK8sLogs_Success(t *testing.T) {
	mockK8sClient := utilmocks.NewMockK8sClient(t)

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Spec: &v1.ClusterSpec{
			Type: v1.KubernetesClusterType,
		},
	}

	lines := int64(1000)
	options := &corev1.PodLogOptions{Container: "serve", TailLines: &lines, Timestamps: true}
	logContent := "k8s log line 1\nk8s log line 2\n"
	mockK8sClient.On("GetPodLogs", mock.Anything, cluster, mock.Anything, "pod-123", options).
		Return(io.NopCloser(strings.NewReader(logContent)), nil).Once()

	ginContext, recorder := createLogsMockContext(http.MethodGet, "/logs")

	err := streamK8sLogs(ginContext, cluster, mockK8sClient, "pod-123", options)

	assert.NoError(t, err)
	assert.Equal(t, logContent, recorder.Body.String())
}

func TestHandleK8sLogs_GetPodError(t *testing.T) {
	router, kubernetesClient := newK8sLogsTestRouter(t)
	kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").
		Return((*corev1.Pod)(nil), errors.New("kubernetes unavailable")).Once()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.JSONEq(t, `{"error":"failed to get pod pod-123: kubernetes unavailable"}`, recorder.Body.String())
	assert.Empty(t, recorder.Header().Get("Content-Disposition"))
}

func TestPrepareK8sLogOptions_NoContainers(t *testing.T) {
	mockK8sClient := utilmocks.NewMockK8sClient(t)

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Spec: &v1.ClusterSpec{
			Type: v1.KubernetesClusterType,
		},
	}

	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-endpoint",
		},
	}

	pod := &corev1.Pod{ObjectMeta: endpointLogTestPod().ObjectMeta}
	mockK8sClient.On("GetPod", mock.Anything, cluster, mock.Anything, "pod-123").Return(pod, nil).Once()

	ginContext, _ := createLogsMockContext(http.MethodGet, "/logs")

	options, err := prepareK8sLogOptions(ginContext, cluster, mockK8sClient, endpoint, "pod-123", "logs", 1000)

	assert.Nil(t, options)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no containers found")
}

func TestHandleK8sLogs_GetPodLogsError(test *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{
			name: "previous unavailable", err: apierrors.NewBadRequest("previous terminated container serve not found"),
			status: http.StatusBadRequest,
		},
		{
			name: "pod removed during acquisition", err: apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "pod-123"),
			status: http.StatusNotFound,
		},
		{name: "upstream unavailable", err: errors.New("kubernetes unavailable"), status: http.StatusInternalServerError},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			router, kubernetesClient := newK8sLogsTestRouter(test)
			kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(endpointLogTestPod(), nil).Once()
			kubernetesClient.On("GetPodLogs", mock.Anything, mock.Anything, mock.Anything, "pod-123", mock.Anything).
				Return(nil, testCase.err).Once()
			request := httptest.NewRequest(http.MethodGet,
				"/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?container=serve&previous=true&download=true", nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			require.Equal(test, testCase.status, recorder.Code)
			assert.JSONEq(test, fmt.Sprintf(`{"error":%q}`, "failed to stream logs: "+testCase.err.Error()), recorder.Body.String())
			assert.Equal(test, "application/json; charset=utf-8", recorder.Header().Get("Content-Type"))
			assert.Empty(test, recorder.Header().Get("Content-Disposition"))
			assert.Empty(test, recorder.Header().Get("Transfer-Encoding"))
		})
	}
}

func TestHandleK8sLogs_StreamErrorAfterWrite(test *testing.T) {
	router, kubernetesClient := newK8sLogsTestRouter(test)
	kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(endpointLogTestPod(), nil).Once()
	stream := io.NopCloser(io.MultiReader(strings.NewReader("partial log\n"), iotest.ErrReader(errors.New("stream interrupted"))))
	kubernetesClient.On("GetPodLogs", mock.Anything, mock.Anything, mock.Anything, "pod-123", mock.Anything).Return(stream, nil).Once()
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?container=serve&previous=true&download=true", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	assert.Equal(test, http.StatusOK, recorder.Code)
	assert.Equal(test, "partial log\n", recorder.Body.String())
	assert.Equal(test, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"))
	assert.Contains(test, recorder.Header().Get("Content-Disposition"), "serve-previous.log")
}

// ===== Tests for handleGetLogSources with mock HTTP client =====

func TestHandleGetLogSources_SSHCluster_Success(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)

	deps := &Dependencies{
		Storage:    mockStorage,
		HTTPClient: mockHTTPClient,
		K8sClient:  &util.DefaultK8sClient{},
	}

	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-endpoint",
		},
		Spec: &v1.EndpointSpec{
			Cluster: "test-cluster",
		},
	}

	cluster := v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Spec: &v1.ClusterSpec{
			Type: v1.SSHClusterType,
		},
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard:8265",
		},
	}

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)
	mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)

	// Mock successful HTTP response
	responseBody := `{
		"applications": {
			"default_test-endpoint": {
				"name": "default_test-endpoint",
				"deployments": {
					"deployment-1": {
						"replicas": [
							{
								"replica_id": "replica-1"
							}
						]
					}
				}
			}
		}
	}`
	mockHTTPClient.On("Get", mock.Anything).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(responseBody)),
		Header:     make(http.Header),
	}, nil)

	c, w := createLogsMockContext("GET", "/api/v1/endpoints/default/test-endpoint/log-sources")

	handler := handleGetLogSources(deps)
	handler(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response LogSourcesResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.NotEmpty(t, response.Deployments)

	mockStorage.AssertExpectations(t)
}

// ===== Tests for handleGetLogs with mock clients =====

func TestHandleGetLogs_SSHCluster_Success(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)

	deps := &Dependencies{
		Storage:    mockStorage,
		HTTPClient: mockHTTPClient,
		K8sClient:  &util.DefaultK8sClient{},
	}

	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-endpoint",
		},
		Spec: &v1.EndpointSpec{
			Cluster: "test-cluster",
		},
	}

	cluster := v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Spec: &v1.ClusterSpec{
			Type: v1.SSHClusterType,
		},
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard:8265",
		},
	}

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)
	mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)

	// Mock applications API response
	appsResponse := `{
		"applications": {
			"default_test-endpoint": {
				"deployments": {
					"deployment-1": {
						"replicas": [
							{
								"replica_id": "replica-1",
								"node_id": "node-1",
								"actor_id": "actor-1",
								"log_file_path": "/tmp/logs/application.log"
							}
						]
					}
				}
			}
		}
	}`
	mockHTTPClient.On("Get", "http://ray-dashboard:8265/api/serve/applications/").Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(appsResponse)),
		Header:     make(http.Header),
	}, nil).Once()

	// Mock successful log stream
	logContent := "ray log line 1\nray log line 2\n"
	mockHTTPClient.On("Get", mock.MatchedBy(func(url string) bool {
		return strings.Contains(url, "api/v0/logs/file")
	})).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(logContent)),
		Header:     make(http.Header),
	}, nil).Once()

	c, w := createLogsMockContext("GET", "/api/v1/endpoints/default/test-endpoint/logs/replica-1/application")

	handler := handleGetLogs(deps)
	handler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, logContent, w.Body.String())

	mockStorage.AssertExpectations(t)
}

func TestHandleGetLogs_SSHCluster_StreamError(test *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		response *http.Response
		err      error
		message  string
	}{
		{name: "connection error", err: errors.New("timeout"), message: "failed to fetch logs: timeout"},
		{
			name: "upstream error", message: "Ray logs API returned status 500",
			response: &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("unavailable"))},
		},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			store := mocks.NewMockStorage(test)
			httpClient := utilmocks.NewMockHTTPClient(test)
			endpoint := v1.Endpoint{
				Metadata: &v1.Metadata{Workspace: "default", Name: "test-endpoint"},
				Spec:     &v1.EndpointSpec{Cluster: "test-cluster"},
			}
			cluster := v1.Cluster{
				Metadata: &v1.Metadata{Workspace: "default", Name: "test-cluster"},
				Spec:     &v1.ClusterSpec{Type: v1.SSHClusterType},
				Status:   &v1.ClusterStatus{DashboardURL: "http://ray-dashboard:8265"},
			}
			store.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil).Once()
			store.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil).Once()
			appsResponse := `{"applications":{"default_test-endpoint":{"deployments":{"Backend":{"replicas":[{"replica_id":"replica-1","actor_id":"actor-1"}]}}}}}`
			httpClient.On("Get", "http://ray-dashboard:8265/api/serve/applications/").Return(&http.Response{
				StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(appsResponse)),
			}, nil).Once()
			httpClient.On("Get", "http://ray-dashboard:8265/api/v0/logs/file?actor_id=actor-1&suffix=err&lines=1000&format=text").
				Return(testCase.response, testCase.err).Once()
			router := gin.New()
			router.GET("/api/v1/endpoints/:workspace/:name/logs/:replica_id/:log_type",
				handleGetLogs(&Dependencies{Storage: store, HTTPClient: httpClient}))
			request := httptest.NewRequest(http.MethodGet,
				"/api/v1/endpoints/default/test-endpoint/logs/replica-1/stderr?download=true", nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			require.Equal(test, http.StatusInternalServerError, recorder.Code)
			assert.JSONEq(test, fmt.Sprintf(`{"error":%q}`, testCase.message), recorder.Body.String())
			assert.Equal(test, "application/json; charset=utf-8", recorder.Header().Get("Content-Type"))
			assert.Empty(test, recorder.Header().Get("Content-Disposition"))
			assert.Empty(test, recorder.Header().Get("Transfer-Encoding"))
		})
	}
}

func TestHandleGetLogs_K8sCluster_Success(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	mockK8sClient := utilmocks.NewMockK8sClient(t)

	deps := &Dependencies{
		Storage:    mockStorage,
		HTTPClient: &util.DefaultHTTPClient{},
		K8sClient:  mockK8sClient,
	}

	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-endpoint",
		},
		Spec: &v1.EndpointSpec{
			Cluster: "test-cluster",
		},
	}

	cluster := v1.Cluster{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-cluster",
		},
		Spec: &v1.ClusterSpec{
			Type: v1.KubernetesClusterType,
		},
	}

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)
	mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)

	// Mock successful pod get
	pod := endpointLogTestPod()
	mockK8sClient.On("GetPod", mock.Anything, &cluster, mock.Anything, "pod-123").Return(pod, nil)

	// Mock successful log stream
	logContent := "k8s log line 1\nk8s log line 2\n"
	mockK8sClient.On("GetPodLogs", mock.Anything, &cluster, mock.Anything, "pod-123", mock.Anything).
		Return(io.NopCloser(strings.NewReader(logContent)), nil)

	c, w := createLogsMockContext("GET", "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs")

	handler := handleGetLogs(deps)
	handler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, logContent, w.Body.String())

	mockStorage.AssertExpectations(t)
}

// ===== Failed-actor fallback integration tests =====
//
// Pure helper tests for the dashboard package functions
// (ExtractReplicaShortID, ReplicaShortIDFromActor, FindFailedActor*,
// LookupFailedActorAcrossDeployments) live in
// internal/ray/dashboard/serve_replica_test.go. The tests here verify
// the routes-layer integration: getRayLogSources synthesizing a
// failed=true ReplicaInfo, streamRayLogs resolving a recovered
// actor_id, and the error path users see when no DEAD actor matches.

func TestGetRayLogSources_FailedActor_FallbackToDashboardActors(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)
	mockDashboardSvc := dashboardmocks.NewMockDashboardService(t)

	originalFactory := dashboard.NewDashboardService
	defer func() { dashboard.NewDashboardService = originalFactory }()
	dashboard.NewDashboardService = func(_ string) dashboard.DashboardService {
		return mockDashboardSvc
	}

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "test-cluster"},
		Status:   &v1.ClusterStatus{DashboardURL: "http://ray-dashboard:8265"},
	}

	// Serve API returns a deployment with NO live replicas (actor crashed and was removed).
	appsResponse := `{
		"applications": {
			"default_test-endpoint": {
				"name": "default_test-endpoint",
				"deployments": {
					"deploy-1": {
						"replicas": []
					}
				}
			}
		}
	}`
	mockHTTPClient.On("Get", mock.MatchedBy(func(url string) bool {
		return strings.Contains(url, "/api/serve/applications/")
	})).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(appsResponse)),
		Header:     make(http.Header),
	}, nil).Once()

	// Dashboard /api/v0/actors returns one DEAD actor that belongs to deploy-1.
	mockDashboardSvc.EXPECT().
		ListActors(mock.MatchedBy(func(filters []dashboard.ActorFilter) bool {
			hasClass, hasState := false, false
			for _, f := range filters {
				if f.Key == "class_name" && f.Value == "ServeReplica:default_test-endpoint:deploy-1" && f.Predicate == "=" {
					hasClass = true
				}
				if f.Key == "state" && f.Value == "DEAD" && f.Predicate == "=" {
					hasState = true
				}
			}
			return hasClass && hasState
		}), true, mock.Anything).
		Return(&dashboard.ActorsResponse{
			Data: dashboard.ActorsResponseData{
				Result: dashboard.ActorsListResult{
					Total: 1,
					Result: []dashboard.Actor{
						{
							ActorID:   "actor-aaa",
							ClassName: "ServeReplica:default_test-endpoint:deploy-1",
							State:     "DEAD",
							Name:      "SERVE_REPLICA::default_test-endpoint#deploy-1#shortxyz",
							NodeID:    "node-1",
						},
					},
				},
			},
		}, nil).
		Once()

	resp, err := getRayLogSources(cluster, mockHTTPClient, "default", "test-endpoint")

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Deployments, 1)
	dep := resp.Deployments[0]
	require.Len(t, dep.Replicas, 1, "expected one synthesized failed replica")
	r := dep.Replicas[0]
	assert.Equal(t, "shortxyz", r.ReplicaID, "replica_id should be the short_id from actor.Name")
	assert.True(t, r.Failed, "synthesized replica must be marked Failed=true")
	require.NotEmpty(t, r.Logs)
	logTypes := map[string]bool{}
	for _, l := range r.Logs {
		logTypes[l.Type] = true
	}
	assert.True(t, logTypes["stderr"], "failed replica should expose stderr log")
	assert.True(t, logTypes["stdout"], "failed replica should expose stdout log")
}

func TestGetRayLogSources_FailedActor_NoDeadActors_ReturnsEmptyReplicas(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)
	mockDashboardSvc := dashboardmocks.NewMockDashboardService(t)

	originalFactory := dashboard.NewDashboardService
	defer func() { dashboard.NewDashboardService = originalFactory }()
	dashboard.NewDashboardService = func(_ string) dashboard.DashboardService {
		return mockDashboardSvc
	}

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "test-cluster"},
		Status:   &v1.ClusterStatus{DashboardURL: "http://ray-dashboard:8265"},
	}

	mockHTTPClient.On("Get", mock.Anything).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"applications":{"default_ep":{"deployments":{"d":{"replicas":[]}}}}}`)),
		Header:     make(http.Header),
	}, nil).Once()

	mockDashboardSvc.EXPECT().ListActors(mock.Anything, true, mock.Anything).Return(&dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{Result: nil}},
	}, nil).Once()

	resp, err := getRayLogSources(cluster, mockHTTPClient, "default", "ep")

	require.NoError(t, err)
	require.Len(t, resp.Deployments, 1)
	assert.Empty(t, resp.Deployments[0].Replicas, "no DEAD actor and no live replica -> empty replicas, no error")
}

func TestStreamRayLogs_FailedActor_ResolvesActorIDByReplicaID(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)
	mockDashboardSvc := dashboardmocks.NewMockDashboardService(t)

	originalFactory := dashboard.NewDashboardService
	defer func() { dashboard.NewDashboardService = originalFactory }()
	dashboard.NewDashboardService = func(_ string) dashboard.DashboardService {
		return mockDashboardSvc
	}

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "test-cluster"},
		Status:   &v1.ClusterStatus{DashboardURL: "http://ray-dashboard:8265"},
	}

	// Live serve API has no replicas in the deployment.
	mockHTTPClient.On("Get", mock.MatchedBy(func(url string) bool {
		return strings.Contains(url, "/api/serve/applications/")
	})).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"applications":{"default_ep":{"deployments":{"d":{"replicas":[]}}}}}`)),
		Header:     make(http.Header),
	}, nil).Once()

	// Dashboard returns a DEAD actor whose name encodes the requested replica_id.
	mockDashboardSvc.EXPECT().ListActors(mock.Anything, true, mock.Anything).Return(&dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{
			Result: dashboard.ActorsListResult{
				Result: []dashboard.Actor{
					{ActorID: "dead-actor-id", State: "DEAD", NodeID: "node-x", Name: "SERVE_REPLICA::default_ep#d#wantedshort"},
				},
			},
		},
	}, nil).Once()

	// Log fetch URL must use the recovered actor_id.
	logContent := "Traceback (most recent call last):\n  ...\nFileNotFoundError: model not found\n"
	mockHTTPClient.On("Get", mock.MatchedBy(func(url string) bool {
		return strings.Contains(url, "actor_id=dead-actor-id") && strings.Contains(url, "suffix=err")
	})).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(logContent)),
		Header:     make(http.Header),
	}, nil).Once()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	err := streamRayLogs(c, cluster, mockHTTPClient, "default", "ep", "wantedshort", "stderr", 500)

	require.NoError(t, err)
	assert.Equal(t, logContent, w.Body.String())
}

func TestStreamRayLogs_FailedActor_NotFoundReturnsError(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)
	mockDashboardSvc := dashboardmocks.NewMockDashboardService(t)

	originalFactory := dashboard.NewDashboardService
	defer func() { dashboard.NewDashboardService = originalFactory }()
	dashboard.NewDashboardService = func(_ string) dashboard.DashboardService {
		return mockDashboardSvc
	}

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "test-cluster"},
		Status:   &v1.ClusterStatus{DashboardURL: "http://ray-dashboard:8265"},
	}

	mockHTTPClient.On("Get", mock.MatchedBy(func(url string) bool {
		return strings.Contains(url, "/api/serve/applications/")
	})).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"applications":{"default_ep":{"deployments":{"d":{"replicas":[]}}}}}`)),
		Header:     make(http.Header),
	}, nil).Once()

	mockDashboardSvc.EXPECT().ListActors(mock.Anything, true, mock.Anything).Return(&dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{Result: dashboard.ActorsListResult{Result: nil}},
	}, nil).Once()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	err := streamRayLogs(c, cluster, mockHTTPClient, "default", "ep", "missing-replica", "stderr", 500)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "replica missing-replica not found",
		"error must name the missing replica so the user can correlate it with their request")
}

func TestStreamRayLogs_FailedActor_ResolvesByActorIDFallback(t *testing.T) {
	mockHTTPClient := utilmocks.NewMockHTTPClient(t)
	mockDashboardSvc := dashboardmocks.NewMockDashboardService(t)

	originalFactory := dashboard.NewDashboardService
	defer func() { dashboard.NewDashboardService = originalFactory }()
	dashboard.NewDashboardService = func(_ string) dashboard.DashboardService {
		return mockDashboardSvc
	}

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "test-cluster"},
		Status:   &v1.ClusterStatus{DashboardURL: "http://ray-dashboard:8265"},
	}

	mockHTTPClient.On("Get", mock.MatchedBy(func(url string) bool {
		return strings.Contains(url, "/api/serve/applications/")
	})).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"applications":{"default_ep":{"deployments":{"d":{"replicas":[]}}}}}`)),
		Header:     make(http.Header),
	}, nil).Once()

	// The DEAD actor has an unconventional Name (no '#' separator), so
	// extractReplicaShortID returns "" and getRayLogSources would have
	// synthesized a ReplicaInfo with replica_id=actor_id. The click-back
	// flow must still resolve via the ActorID fallback path.
	mockDashboardSvc.EXPECT().ListActors(mock.Anything, true, mock.Anything).Return(&dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{
			Result: dashboard.ActorsListResult{
				Result: []dashboard.Actor{
					{ActorID: "weirdactor-id", State: "DEAD", NodeID: "node-y", Name: "non-conventional-actor-name"},
				},
			},
		},
	}, nil).Once()

	logContent := "actor stderr content\n"
	mockHTTPClient.On("Get", mock.MatchedBy(func(url string) bool {
		return strings.Contains(url, "actor_id=weirdactor-id")
	})).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(logContent)),
		Header:     make(http.Header),
	}, nil).Once()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	err := streamRayLogs(c, cluster, mockHTTPClient, "default", "ep", "weirdactor-id", "stderr", 500)

	require.NoError(t, err)
	assert.Equal(t, logContent, w.Body.String())
}

func TestGetK8sLogSources_Metadata(test *testing.T) {
	cluster := logSourcesTestCluster(test, []corev1.Pod{*endpointLogTestPod()})
	endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Workspace: "default", Name: "test-endpoint"}}
	response, err := getK8sLogSources(cluster, endpoint, "default", "test-endpoint")
	require.NoError(test, err)
	encoded, err := json.Marshal(response)
	require.NoError(test, err)
	assert.JSONEq(test, `{"deployments":[{"name":"Backend","replicas":[{
				"replica_id":"pod-123",
				"logs":[{"type":"logs","container_name":"serve","restart_count":1,"started":true,
				"url":"/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?container=serve",
				"download_url":"/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?container=serve&download=true"},
				{"type":"logs","container_name":"sidecar",
				"url":"/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?container=sidecar",
				"download_url":"/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?container=sidecar&download=true"},
				{"type":"logs","container_name":"model-downloader","restart_count":1,"started":true,
				"url":"/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?container=model-downloader",
				"download_url":"/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?container=model-downloader&download=true"}]
			}]}]}`, string(encoded))
}

func logSourcesTestCluster(test *testing.T, pods []corev1.Pod) *v1.Cluster {
	test.Helper()
	cluster := &v1.Cluster{Metadata: &v1.Metadata{Workspace: "default", Name: "test-cluster"}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		var response interface{}
		switch request.URL.Path {
		case "/api":
			response = metav1.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			response = metav1.APIGroupList{}
		case "/api/v1":
			response = metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
				{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"list"}},
			}}
		case "/api/v1/namespaces/" + util.ClusterNamespace(cluster) + "/pods":
			assert.Equal(test, "app=inference,endpoint=test-endpoint,workspace=default", request.URL.Query().Get("labelSelector"))
			response = corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: pods}
		default:
			test.Errorf("unexpected Kubernetes API request: %s", request.URL)
			http.NotFound(writer, request)

			return
		}
		assert.NoError(test, json.NewEncoder(writer).Encode(response))
	}))
	test.Cleanup(server.Close)
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
contexts:
- name: test
  context:
    cluster: test
current-context: test
`, server.URL)
	cluster.Spec = &v1.ClusterSpec{Type: v1.KubernetesClusterType, Config: &v1.ClusterConfig{
		KubernetesConfig: &v1.KubernetesClusterConfig{Kubeconfig: base64.StdEncoding.EncodeToString([]byte(kubeconfig))},
	}}

	return cluster
}

func newK8sLogsTestRouter(test *testing.T) (*gin.Engine, *utilmocks.MockK8sClient) {
	test.Helper()
	gin.SetMode(gin.TestMode)
	store := new(mocks.MockStorage)
	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{Workspace: "default", Name: "test-endpoint"},
		Spec:     &v1.EndpointSpec{Cluster: "test-cluster"},
	}
	cluster := v1.Cluster{
		Metadata: &v1.Metadata{Workspace: "default", Name: "test-cluster"},
		Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType},
	}
	store.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil).Once()
	store.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil).Once()
	store.On("CallDatabaseFunction", "has_permission", map[string]interface{}{
		"user_uuid": "log-reader", "required_permission": "endpoint:read", "workspace": "default",
	}, mock.Anything).Run(func(arguments mock.Arguments) {
		permission, ok := arguments.Get(2).(*bool)
		require.True(test, ok)
		*permission = true
	}).Return(nil).Once()
	test.Cleanup(func() { store.AssertExpectations(test) })
	kubernetesClient := utilmocks.NewMockK8sClient(test)
	router := gin.New()
	identity := func(ginContext *gin.Context) { ginContext.Set("user_id", "log-reader") }
	RegisterEndpointLogsRoutes(router.Group("/api/v1"), []gin.HandlerFunc{identity}, &Dependencies{Storage: store, K8sClient: kubernetesClient})

	return router, kubernetesClient
}

func endpointLogTestPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "pod-123",
			Labels: map[string]string{"workspace": "default", "endpoint": "test-endpoint", "app": "inference"},
		},
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "serve"}, {Name: "sidecar"}},
			InitContainers: []corev1.Container{{Name: "model-downloader"}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "sidecar"}, {Name: "serve", RestartCount: 1},
			},
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name: "model-downloader", RestartCount: 1,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			}},
		},
	}
}

func TestHandleK8sLogs_ContainerSelection(test *testing.T) {
	tests := []struct {
		name      string
		query     string
		method    string
		container string
		previous  bool
		lines     int64
		download  bool
	}{
		{name: "default main current", container: "serve", lines: 1000},
		{
			name: "sidecar current download", query: "container=sidecar&previous=false&download=true",
			container: "sidecar", lines: 1000, download: true,
		},
		{name: "main previous", query: "container=serve&previous=true", container: "serve", previous: true, lines: 1000},
		{name: "completed init current", query: "container=model-downloader", container: "model-downloader", lines: 1000},
		{
			name: "completed init previous download", query: "container=model-downloader&previous=true&lines=25&download=true",
			container: "model-downloader", previous: true, lines: 25, download: true,
		},
		{
			name: "head validates selection", method: http.MethodHead,
			query: "container=model-downloader&previous=true&download=true", container: "model-downloader",
			previous: true, download: true,
		},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			router, kubernetesClient := newK8sLogsTestRouter(test)
			kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(endpointLogTestPod(), nil).Once()
			content := fmt.Sprintf("container=%s previous=%t\n", testCase.container, testCase.previous)
			if testCase.method != http.MethodHead {
				kubernetesClient.On("GetPodLogs", mock.Anything, mock.Anything, mock.Anything, "pod-123",
					mock.MatchedBy(func(options *corev1.PodLogOptions) bool {
						return options.Container == testCase.container && options.Previous == testCase.previous &&
							options.Timestamps && options.TailLines != nil && *options.TailLines == testCase.lines && !options.Follow
					})).Return(io.NopCloser(strings.NewReader(content)), nil).Once()
			}
			method := testCase.method
			if method == "" {
				method = http.MethodGet
			}
			request := httptest.NewRequest(method, "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs?"+testCase.query, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			require.Equal(test, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(test, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"))
			if method == http.MethodHead {
				assert.Empty(test, recorder.Body.String())
			} else {
				assert.Equal(test, content, recorder.Body.String())
			}
			if testCase.download {
				mode := "current"
				if testCase.previous {
					mode = "previous"
				}
				assert.Contains(test, recorder.Header().Get("Content-Disposition"), "pod-123-"+testCase.container+"-"+mode+".log")
			} else {
				assert.Empty(test, recorder.Header().Get("Content-Disposition"))
			}
		})
	}
}

func TestHandleK8sLogs_PodOwnership(test *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
	}{
		{
			name: "other endpoint", pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"workspace": "default", "endpoint": "other", "app": "inference"},
			}},
		},
		{
			name: "other workspace", pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"workspace": "other", "endpoint": "test-endpoint", "app": "inference"},
			}},
		},
		{
			name: "not an inference pod", pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{"workspace": "default", "endpoint": "test-endpoint", "app": "system"},
			}},
		},
	}
	for _, testCase := range tests {
		test.Run(testCase.name, func(test *testing.T) {
			router, kubernetesClient := newK8sLogsTestRouter(test)
			kubernetesClient.On("GetPod", mock.Anything, mock.Anything, mock.Anything, "pod-123").Return(testCase.pod, nil).Once()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs", nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			require.Equal(test, http.StatusBadRequest, recorder.Code)
			assert.JSONEq(test, `{"error":"pod does not belong to this endpoint"}`, recorder.Body.String())
			assert.Empty(test, recorder.Header().Get("Content-Disposition"))
		})
	}
}

func TestEndpointLogRoutes_RequiresReadPermission(test *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "log-sources"},
		{method: http.MethodGet, path: "logs/pod-123/logs?container=model-downloader&previous=true&download=true"},
		{method: http.MethodHead, path: "logs/pod-123/logs?download=true"},
	}
	workspace := "other-workspace"
	for _, route := range routes {
		test.Run(route.method+"/"+route.path, func(test *testing.T) {
			gin.SetMode(gin.TestMode)
			store := mocks.NewMockStorage(test)
			store.On("CallDatabaseFunction", "has_permission", map[string]interface{}{
				"user_uuid": "user-123", "required_permission": "endpoint:read", "workspace": workspace,
			}, mock.Anything).Return(nil).Once()

			kubernetesClient := utilmocks.NewMockK8sClient(test)
			httpClient := utilmocks.NewMockHTTPClient(test)
			identity := func(context *gin.Context) { context.Set("user_id", "user-123") }
			router := gin.New()
			RegisterEndpointLogsRoutes(router.Group("/api/v1"), []gin.HandlerFunc{identity}, &Dependencies{
				Storage: store, K8sClient: kubernetesClient, HTTPClient: httpClient,
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(route.method, "/api/v1/endpoints/"+workspace+"/test-endpoint/"+route.path, nil)
			router.ServeHTTP(recorder, request)

			assert.Equal(test, http.StatusForbidden, recorder.Code, recorder.Body.String())
			store.AssertNotCalled(test, "ListEndpoint", mock.Anything)
			store.AssertNotCalled(test, "ListCluster", mock.Anything)
			assert.Empty(test, kubernetesClient.Calls)
			assert.Empty(test, httpClient.Calls)
		})
	}
}
