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

// createMockContext creates a mock Gin context for testing
func createMockContext(method, path string, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Setup request with params
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}

	c.Request = req

	// Parse URL path parameters
	parts := strings.Split(path, "/")
	if len(parts) >= 4 {
		name := parts[4] // Extract name from URL pattern /api/v1/xxx-proxy/:name/*path
		c.Params = []gin.Param{
			{
				Key:   "name",
				Value: name,
			},
		}

		// Extract path parameter if available
		if len(parts) > 5 {
			restPath := "/" + strings.Join(parts[5:], "/")
			c.Params = append(c.Params, gin.Param{
				Key:   "path",
				Value: restPath,
			})
		}
	}

	return c, w
}

// setupMocks creates and configures mocks for testing
func setupMocks(t *testing.T) *mocks.MockStorage {
	// Create mocks
	mockStorage := new(mocks.MockStorage)

	return mockStorage
}

// TestCreateProxyHandler tests the error case for CreateProxyHandler
func TestCreateProxyHandler(t *testing.T) {
	// Only test the error case which doesn't use the actual proxy
	t.Run("Invalid URL", func(t *testing.T) {
		// Create handler with invalid URL
		handler := CreateProxyHandler("://invalid-url", "", nil)

		// Create test context
		c, w := createMockContext("GET", "/test", "")

		// Call the handler
		handler(c)

		// Verify response - should return error
		assert.Equal(t, http.StatusInternalServerError, w.Code)

		var response map[string]string
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)
		assert.Equal(t, "Failed to create proxy", response["error"])
	})
}

// TestHandleServeProxy_MissingName tests the case when name parameter is missing
func TestHandleServeProxy_MissingName(t *testing.T) {
	// Setup mock
	mockStorage := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Create test context without name
	c, w := createMockContext("GET", "/api/v1/serve-proxy/", "")

	// Call the handler function directly
	handlerFunc := handleServeProxy(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "name is required", response["error"])
}

// TestHandleServeProxy_EndpointNotFound tests the case when endpoint is not found
func TestHandleServeProxy_EndpointNotFound(t *testing.T) {
	// Setup mock
	mockStorage := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Configure mock behaviors - return empty result
	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)

	// Create test context
	c, w := createMockContext("GET", "/api/v1/serve-proxy/non-existent-endpoint", "")

	// Call the handler function directly
	handlerFunc := handleServeProxy(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusNotFound, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "endpoint not found", response["error"])

	mockStorage.AssertExpectations(t)
}

// TestHandleServeProxy_StorageError tests the case when storage returns an error
func TestHandleServeProxy_StorageError(t *testing.T) {
	// Setup mock
	mockStorage := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Configure mock behaviors - return error
	mockError := errors.New("storage error")
	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, mockError)

	// Create test context
	c, w := createMockContext("GET", "/api/v1/serve-proxy/test-endpoint", "")

	// Call the handler function directly
	handlerFunc := handleServeProxy(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["error"], "Failed to list endpoints")

	mockStorage.AssertExpectations(t)
}

// TestHandleServeProxy_MissingServiceURL tests the case when service URL is missing
func TestHandleServeProxy_MissingServiceURL(t *testing.T) {
	// Setup mock
	mockStorage := setupMocks(t)

	// Create test endpoint without service URL
	endpoint := v1.Endpoint{
		Status: &v1.EndpointStatus{
			// ServiceURL intentionally left empty
		},
	}

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Configure mock behaviors
	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{endpoint}, nil)

	// Create test context
	c, w := createMockContext("GET", "/api/v1/serve-proxy/test-endpoint", "")

	// Call the handler function directly
	handlerFunc := handleServeProxy(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "service_url not found", response["error"])

	mockStorage.AssertExpectations(t)
}

// TestHandleRayDashboardProxy_MissingName tests the case when name parameter is missing
func TestHandleRayDashboardProxy_MissingName(t *testing.T) {
	// Setup mock
	mockStorage := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Create test context without name
	c, w := createMockContext("GET", "/api/v1/ray-dashboard-proxy/", "")

	// Call the handler function directly
	handlerFunc := handleRayDashboardProxy(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "name is required", response["error"])
}

// TestHandleRayDashboardProxy_ClusterNotFound tests the case when cluster is not found
func TestHandleRayDashboardProxy_ClusterNotFound(t *testing.T) {
	// Setup mock
	mockStorage := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Configure mock behaviors - return empty result
	mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{}, nil)

	// Create test context
	c, w := createMockContext("GET", "/api/v1/ray-dashboard-proxy/non-existent-cluster", "")

	// Call the handler function directly
	handlerFunc := handleRayDashboardProxy(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusNotFound, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "cluster not found", response["error"])

	mockStorage.AssertExpectations(t)
}

// TestHandleRayDashboardProxy_StorageError tests the case when storage returns an error
func TestHandleRayDashboardProxy_StorageError(t *testing.T) {
	// Setup mock
	mockStorage := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Configure mock behaviors - return error
	mockError := errors.New("storage error")
	mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{}, mockError)

	// Create test context
	c, w := createMockContext("GET", "/api/v1/ray-dashboard-proxy/test-cluster", "")

	// Call the handler function directly
	handlerFunc := handleRayDashboardProxy(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["error"], "Failed to list clusters")

	mockStorage.AssertExpectations(t)
}

// TestHandleRayDashboardProxy_MissingDashboardURL tests the case when dashboard URL is missing
func TestHandleRayDashboardProxy_MissingDashboardURL(t *testing.T) {
	// Setup mock
	mockStorage := setupMocks(t)

	// Create test cluster without dashboard URL
	cluster := v1.Cluster{
		Status: &v1.ClusterStatus{
			// DashboardURL intentionally left empty
		},
	}

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Configure mock behaviors
	mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil)

	// Create test context
	c, w := createMockContext("GET", "/api/v1/ray-dashboard-proxy/test-cluster", "")

	// Call the handler function directly
	handlerFunc := handleRayDashboardProxy(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "dashboard_url not found", response["error"])

	mockStorage.AssertExpectations(t)
}
