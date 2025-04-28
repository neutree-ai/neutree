package models

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/model_registry"
	model_registry_mocks "github.com/neutree-ai/neutree/pkg/model_registry/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// createMockContext creates a mock Gin context for testing
func createMockContext(registryName, searchQuery string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Setup request with params
	c.Request = httptest.NewRequest("GET", "/api/v1/search-models/"+registryName+"?search="+searchQuery, nil)
	c.Params = []gin.Param{
		{
			Key:   "name",
			Value: registryName,
		},
	}

	// Add search query
	if searchQuery != "" {
		c.Request.URL.RawQuery = "search=" + searchQuery
	}

	return c, w
}

// setupMocks creates and configures mocks for testing
func setupMocks(t *testing.T) (*mocks.MockStorage, *model_registry_mocks.MockModelRegistry) {
	// Create mocks
	mockStorage := new(mocks.MockStorage)
	mockModelRegistry := new(model_registry_mocks.MockModelRegistry)

	// Replace the model registry factory function
	origNewModelRegistry := model_registry.NewModelRegistry
	model_registry.NewModelRegistry = func(registry *v1.ModelRegistry) (model_registry.ModelRegistry, error) {
		return mockModelRegistry, nil
	}

	// Clean up after the test
	t.Cleanup(func() {
		model_registry.NewModelRegistry = origNewModelRegistry
	})

	return mockStorage, mockModelRegistry
}

func TestSearchModels_Success(t *testing.T) {
	// Setup mocks
	mockStorage, mockModelRegistry := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Create test context
	registryName := "test-registry"
	searchQuery := "test"
	c, w := createMockContext(registryName, searchQuery)

	// Prepare mock data
	modelRegistry := v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: "hugging-face",
		},
		Status: &v1.ModelRegistryStatus{},
	}

	mockModels := []v1.GeneralModel{
		{
			Name:     "Test Model 1",
			Versions: []v1.Version{},
		},
		{
			Name:     "Test Model 2",
			Versions: []v1.Version{},
		},
	}

	// Configure mock behaviors
	mockStorage.On("ListModelRegistry", mock.MatchedBy(func(option storage.ListOption) bool {
		return len(option.Filters) > 0 && option.Filters[0].Column == "metadata->name" &&
			option.Filters[0].Operator == "eq" && option.Filters[0].Value == "\"test-registry\""
	})).Return([]v1.ModelRegistry{modelRegistry}, nil)

	mockModelRegistry.On("ListModels", mock.MatchedBy(func(option model_registry.ListOption) bool {
		return option.Search == "test"
	})).Return(mockModels, nil)

	// Call the handler function directly
	handlerFunc := searchModels(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusOK, w.Code)

	var response []v1.GeneralModel
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(response))
	assert.Equal(t, "Test Model 1", response[0].Name)

	// Verify mock expectations
	mockStorage.AssertExpectations(t)
	mockModelRegistry.AssertExpectations(t)
}

func TestSearchModels_RegistryNotFound(t *testing.T) {
	// Setup mocks
	mockStorage, _ := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Create test context
	c, w := createMockContext("non-existent-registry", "")

	// Configure mock behaviors - return empty result
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{}, nil)

	// Call the handler function directly
	handlerFunc := searchModels(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusNotFound, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "model registry not found", response["message"])

	mockStorage.AssertExpectations(t)
}

func TestSearchModels_StorageError(t *testing.T) {
	// Setup mocks
	mockStorage, _ := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Create test context
	c, w := createMockContext("test-registry", "")

	// Configure mock behaviors - return error
	mockError := errors.New("storage error")
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{}, mockError)

	// Call the handler function directly
	handlerFunc := searchModels(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["message"], "Failed to list model registries")

	mockStorage.AssertExpectations(t)
}

func TestSearchModels_ListModelsError(t *testing.T) {
	// Setup mocks
	mockStorage, mockModelRegistry := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
	}

	// Create test context
	c, w := createMockContext("test-registry", "test-query")

	// Prepare mock data
	modelRegistry := v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: "hugging-face",
		},
	}

	// Configure mock behaviors
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{modelRegistry}, nil)

	// Configure list models to return error
	mockError := errors.New("list models error")
	mockModelRegistry.On("ListModels", mock.Anything).Return(nil, mockError)

	// Call the handler function directly
	handlerFunc := searchModels(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["message"], "Failed to list models")

	mockStorage.AssertExpectations(t)
	mockModelRegistry.AssertExpectations(t)
}
