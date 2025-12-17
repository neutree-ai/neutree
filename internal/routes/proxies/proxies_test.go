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
		workspace := parts[4] // Extract workspace from URL pattern /api/v1/xxx-proxy/:workspace/:name/*path
		name := parts[5]      // Extract name from URL pattern /api/v1/xxx-proxy/:workspace/:name/*path
		c.Params = []gin.Param{
			{
				Key:   "workspace",
				Value: workspace,
			},
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
	c, w := createMockContext("GET", "/api/v1/serve-proxy/default/", "")

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
	c, w := createMockContext("GET", "/api/v1/serve-proxy/default/non-existent-endpoint/", "")

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
	c, w := createMockContext("GET", "/api/v1/serve-proxy/default/test-endpoint", "")

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

// TestHandleServeProxy_MissingServiceURL tests the case when the service URL is missing
func TestHandleServeProxy_MissingClusterDashboardURL(t *testing.T) {
	// Setup mock
	mockStorage := setupMocks(t)

	// Create test endpoint without service URL
	endpoint := v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "test-workspace",
		},
		Spec: &v1.EndpointSpec{
			Cluster: "test-cluster",
		},
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
	mockStorage.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
		Status: &v1.ClusterStatus{},
	}}, nil)

	// Create test context
	c, w := createMockContext("GET", "/api/v1/serve-proxy/default/test-endpoint", "")

	// Call the handler function directly
	handlerFunc := handleServeProxy(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "cluster dashboard_url not found", response["error"])

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
	c, w := createMockContext("GET", "/api/v1/ray-dashboard-proxy/default/", "")

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
	c, w := createMockContext("GET", "/api/v1/ray-dashboard-proxy/default/non-existent-cluster", "")

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
	c, w := createMockContext("GET", "/api/v1/ray-dashboard-proxy/default/test-cluster", "")

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
	c, w := createMockContext("GET", "/api/v1/ray-dashboard-proxy/default/test-cluster", "")

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

// TestCreatePostgrestAuthModifier tests the CreatePostgrestAuthModifier function
func TestCreatePostgrestAuthModifier(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("With postgrest_token in context", func(t *testing.T) {
		// Create a test context with postgrest_token
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("postgrest_token", "test-postgrest-token-123")

		// Create a test request
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "sk_original_api_key")

		// Apply the modifier
		modifier := CreatePostgrestAuthModifier(c)
		modifier(req)

		// Verify the Authorization header was replaced
		assert.Equal(t, "Bearer test-postgrest-token-123", req.Header.Get("Authorization"))
	})

	t.Run("Without postgrest_token in context", func(t *testing.T) {
		// Create a test context without postgrest_token
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		// Create a test request with original Bearer token
		req := httptest.NewRequest("GET", "/test", nil)
		originalAuth := "Bearer original-jwt-token"
		req.Header.Set("Authorization", originalAuth)

		// Apply the modifier
		modifier := CreatePostgrestAuthModifier(c)
		modifier(req)

		// Verify the Authorization header was not modified
		assert.Equal(t, originalAuth, req.Header.Get("Authorization"))
	})

	t.Run("With empty postgrest_token", func(t *testing.T) {
		// Create a test context with empty postgrest_token
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("postgrest_token", "")

		// Create a test request
		req := httptest.NewRequest("GET", "/test", nil)
		originalAuth := "Bearer original-token"
		req.Header.Set("Authorization", originalAuth)

		// Apply the modifier
		modifier := CreatePostgrestAuthModifier(c)
		modifier(req)

		// Empty postgrest_token means GetPostgrestToken returns false,
		// so Authorization should not be modified
		assert.Equal(t, originalAuth, req.Header.Get("Authorization"))
	})
}

func TestAddPostgrestHeaderModifier(t *testing.T) {
	// Create a test context
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	// Create a test request
	req := httptest.NewRequest("GET", "/test", nil)

	// Apply the modifier
	modifier := AddPostgrestHeaderModifier(c)
	modifier(req)

	// Verify the Accept header was set correctly
	assert.Equal(t, "application/vnd.pgrst.array+json;nulls=stripped", req.Header.Get("Accept"))
}
