package logs

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
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

	c, w := createLogsMockContext("HEAD", "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs")

	handler := handleGetLogs(deps)
	handler(c)

	// HEAD request should return 200 with headers but no body
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "chunked", w.Header().Get("Transfer-Encoding"))
	assert.Empty(t, w.Body.String())

	mockStorage.AssertExpectations(t)
}

func TestHandleGetLogs_DownloadParameter(t *testing.T) {
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
			Type: v1.KubernetesClusterType,
		},
	}

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)
	mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)

	c, w := createLogsMockContext("HEAD", "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs")
	// Set query parameter properly
	c.Request.URL.RawQuery = "download=true"

	handler := handleGetLogs(deps)
	handler(c)

	// Should have Content-Disposition header when download=true
	assert.Equal(t, http.StatusOK, w.Code)
	contentDisposition := w.Header().Get("Content-Disposition")
	assert.Contains(t, contentDisposition, "attachment")
	assert.Contains(t, contentDisposition, "default-test-endpoint-pod-123-logs.log")

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

			c, w := createLogsMockContext("HEAD", "/api/v1/endpoints/default/test-endpoint/logs/pod-123/logs")
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

	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "test-endpoint",
		},
	}

	// Mock successful pod get
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "serve"},
			},
		},
	}
	mockK8sClient.On("GetPod", mock.Anything, cluster, mock.Anything, "pod-123").Return(pod, nil)

	// Mock successful log stream
	logContent := "k8s log line 1\nk8s log line 2\n"
	mockK8sClient.On("GetPodLogs", mock.Anything, cluster, mock.Anything, "pod-123", mock.Anything).
		Return(io.NopCloser(strings.NewReader(logContent)), nil)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// Create a request with context
	req := httptest.NewRequest("GET", "/logs", nil)
	c.Request = req

	err := streamK8sLogs(c, cluster, mockK8sClient, endpoint, "pod-123", "logs", 1000)

	assert.NoError(t, err)
	assert.Equal(t, logContent, w.Body.String())
}

func TestStreamK8sLogs_GetPodError(t *testing.T) {
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

	// Mock GetPod error
	mockK8sClient.On("GetPod", mock.Anything, cluster, mock.Anything, "pod-123").
		Return((*corev1.Pod)(nil), errors.New("pod not found"))

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// Create a request with context
	req := httptest.NewRequest("GET", "/logs", nil)
	c.Request = req

	err := streamK8sLogs(c, cluster, mockK8sClient, endpoint, "pod-123", "logs", 1000)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pod not found")
}

func TestStreamK8sLogs_NoContainers(t *testing.T) {
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

	// Mock pod without any containers
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{},
		},
	}
	mockK8sClient.On("GetPod", mock.Anything, cluster, mock.Anything, "pod-123").Return(pod, nil)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// Create a request with context
	req := httptest.NewRequest("GET", "/logs", nil)
	c.Request = req

	err := streamK8sLogs(c, cluster, mockK8sClient, endpoint, "pod-123", "logs", 1000)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no containers found")
}

func TestStreamK8sLogs_GetPodLogsError(t *testing.T) {
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

	// Mock successful pod get
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "serve"},
			},
		},
	}
	mockK8sClient.On("GetPod", mock.Anything, cluster, mock.Anything, "pod-123").Return(pod, nil)

	// Mock GetPodLogs error
	mockK8sClient.On("GetPodLogs", mock.Anything, cluster, mock.Anything, "pod-123", mock.Anything).
		Return(nil, errors.New("failed to get logs"))

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// Create a request with context
	req := httptest.NewRequest("GET", "/logs", nil)
	c.Request = req

	err := streamK8sLogs(c, cluster, mockK8sClient, endpoint, "pod-123", "logs", 1000)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get logs")
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
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "serve"},
			},
		},
	}
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
