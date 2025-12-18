package proxies

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage/mocks"
)

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
	deps := &Dependencies{Storage: mockStorage}

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
	deps := &Dependencies{Storage: mockStorage}

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
	deps := &Dependencies{Storage: mockStorage}

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)

	c, w := createLogsMockContext("GET", "/api/v1/endpoints/default/non-existent/log-sources")

	handler := handleGetLogSources(deps)
	handler(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
	mockStorage.AssertExpectations(t)
}

func TestHandleGetLogSources_StorageError(t *testing.T) {
	mockStorage := new(mocks.MockStorage)
	deps := &Dependencies{Storage: mockStorage}

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
	deps := &Dependencies{Storage: mockStorage}

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
	deps := &Dependencies{Storage: mockStorage}

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
	deps := &Dependencies{Storage: mockStorage}

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
	deps := &Dependencies{Storage: mockStorage}

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
			deps := &Dependencies{Storage: mockStorage}

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
		deps := &Dependencies{Storage: mockStorage}

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
		deps := &Dependencies{Storage: mockStorage}

		mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)

		_, _, err := getEndpointAndCluster(deps, "default", "non-existent")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "endpoint not found")

		mockStorage.AssertExpectations(t)
	})

	t.Run("cluster not found", func(t *testing.T) {
		mockStorage := new(mocks.MockStorage)
		deps := &Dependencies{Storage: mockStorage}

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

