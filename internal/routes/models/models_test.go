package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	model_registry_mocks "github.com/neutree-ai/neutree/internal/model_registry/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// createMockContext creates a mock Gin context for testing
func createMockContext(workspace, registryName, modelName, searchQuery string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Construct URL based on parameters
	url := "/api/v1/workspaces/" + workspace + "/model_registries/" + registryName + "/models"
	if modelName != "" {
		url += "/" + modelName
	}
	if searchQuery != "" {
		url += "?search=" + searchQuery
	}

	// Setup request with params
	c.Request = httptest.NewRequest("GET", url, nil)
	c.Params = []gin.Param{
		{
			Key:   "workspace",
			Value: workspace,
		},
		{
			Key:   "registry",
			Value: registryName,
		},
	}

	if modelName != "" {
		c.Params = append(c.Params, gin.Param{
			Key:   "model",
			Value: modelName,
		})
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

func endpointModelReferenceFilterMatcher(workspace, registryName, modelName string) interface{} {
	return mock.MatchedBy(func(option storage.ListOption) bool {
		expected := map[string]string{
			"metadata->workspace":    strconvQuote(workspace),
			"spec->model->>registry": registryName,
			"spec->model->>name":     modelName,
		}

		if len(option.Filters) != len(expected) {
			return false
		}

		for _, filter := range option.Filters {
			if filter.Operator != "eq" {
				return false
			}

			value, ok := expected[filter.Column]
			if !ok || filter.Value != value {
				return false
			}
		}

		return true
	})
}

func strconvQuote(value string) string {
	return "\"" + value + "\""
}

func setVersionQuery(c *gin.Context, version string) {
	c.Request.URL.RawQuery = "version=" + version
}

func endpointWithModel(registryName, modelName, version string) v1.Endpoint {
	return v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Model: &v1.ModelSpec{
				Registry: registryName,
				Name:     modelName,
				Version:  version,
			},
		},
	}
}

func TestListModels_Success(t *testing.T) {
	// Setup mocks
	mockStorage, mockModelRegistry := setupMocks(t)

	// Create handler dependencies with a mock temp directory function
	mockTempDir := t.TempDir()
	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return mockTempDir, nil
		},
	}

	// Create test context
	workspace := "default"
	registryName := "test-registry"
	searchQuery := "test"
	c, w := createMockContext(workspace, registryName, "", searchQuery)

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
			Versions: []v1.ModelVersion{},
		},
		{
			Name:     "Test Model 2",
			Versions: []v1.ModelVersion{},
		},
	}

	// Configure mock behaviors
	mockStorage.On("ListModelRegistry", mock.MatchedBy(func(option storage.ListOption) bool {
		return len(option.Filters) == 2 &&
			option.Filters[0].Column == "metadata->workspace" &&
			option.Filters[0].Operator == "eq" &&
			option.Filters[0].Value == "\"default\"" &&
			option.Filters[1].Column == "metadata->name" &&
			option.Filters[1].Operator == "eq" &&
			option.Filters[1].Value == "\"test-registry\""
	})).Return([]v1.ModelRegistry{modelRegistry}, nil)

	mockModelRegistry.On("Connect").Return(nil)
	mockModelRegistry.On("Disconnect").Return(nil)
	mockModelRegistry.On("ListModels", mock.MatchedBy(func(option model_registry.ListOption) bool {
		return option.Search == "test"
	})).Return(mockModels, nil)

	// Call the handler function directly
	handlerFunc := listModels(deps)
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

func TestListModels_RegistryNotFound(t *testing.T) {
	// Setup mocks
	mockStorage, _ := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return t.TempDir(), nil
		},
	}

	// Create test context
	c, w := createMockContext("default", "non-existent-registry", "", "")

	// Configure mock behaviors - return empty result
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{}, nil)

	// Call the handler function directly
	handlerFunc := listModels(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["message"], "model registry not found")

	mockStorage.AssertExpectations(t)
}

func TestListModels_StorageError(t *testing.T) {
	// Setup mocks
	mockStorage, _ := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return t.TempDir(), nil
		},
	}

	// Create test context
	c, w := createMockContext("default", "test-registry", "", "")

	// Configure mock behaviors - return error
	mockError := errors.New("storage error")
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{}, mockError)

	// Call the handler function directly
	handlerFunc := listModels(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["message"], "failed to find model registry")

	mockStorage.AssertExpectations(t)
}

func TestListModels_ListModelsError(t *testing.T) {
	// Setup mocks
	mockStorage, mockModelRegistry := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return t.TempDir(), nil
		},
	}

	// Create test context
	c, w := createMockContext("default", "test-registry", "", "test-query")

	// Prepare mock data
	modelRegistry := v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: "hugging-face",
		},
	}

	// Configure mock behaviors
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{modelRegistry}, nil)

	mockModelRegistry.On("Connect").Return(nil)
	mockModelRegistry.On("Disconnect").Return(nil)
	// Configure list models to return error
	mockError := errors.New("list models error")
	mockModelRegistry.On("ListModels", mock.Anything).Return(nil, mockError)

	// Call the handler function directly
	handlerFunc := listModels(deps)
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

func TestGetModel_Success(t *testing.T) {
	// Setup mocks
	mockStorage, mockModelRegistry := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return t.TempDir(), nil
		},
	}

	// Create test context
	c, w := createMockContext("default", "test-registry", "test-model", "")

	// Prepare mock data
	modelRegistry := v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: "bentoml",
		},
	}

	mockModelVersion := &v1.ModelVersion{
		Name:         v1.LatestVersion,
		CreationTime: "2023-01-01T00:00:00Z",
		Size:         "10MB",
		Module:       "test-module",
	}

	// Configure mock behaviors
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{modelRegistry}, nil)
	mockModelRegistry.On("Connect").Return(nil)
	mockModelRegistry.On("Disconnect").Return(nil)
	mockModelRegistry.On("GetModelVersion", "test-model", v1.LatestVersion).Return(mockModelVersion, nil)

	// Call the handler function directly
	handlerFunc := getModel(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusOK, w.Code)

	// Verify mock expectations
	mockStorage.AssertExpectations(t)
	mockModelRegistry.AssertExpectations(t)
}

func TestGetModel_NotFound(t *testing.T) {
	// Setup mocks
	mockStorage, mockModelRegistry := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return t.TempDir(), nil
		},
	}

	// Create test context
	c, w := createMockContext("default", "test-registry", "non-existent-model", "")

	// Prepare mock data
	modelRegistry := v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: "bentoml",
		},
	}

	// Configure mock behaviors
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{modelRegistry}, nil)
	mockModelRegistry.On("Connect").Return(nil)
	mockModelRegistry.On("Disconnect").Return(nil)
	mockError := errors.New("model not found")
	mockModelRegistry.On("GetModelVersion", "non-existent-model", v1.LatestVersion).Return(nil, mockError)

	// Call the handler function directly
	handlerFunc := getModel(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["message"], "Failed to get model")

	// Verify mock expectations
	mockStorage.AssertExpectations(t)
	mockModelRegistry.AssertExpectations(t)
}

func TestDeleteModel_Success(t *testing.T) {
	// Setup mocks
	mockStorage, mockModelRegistry := setupMocks(t)

	// Create handler dependencies
	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return t.TempDir(), nil
		},
	}

	// Create test context
	c, _ := createMockContext("default", "test-registry", "test-model", "")

	// Prepare mock data
	modelRegistry := v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: "bentoml",
		},
	}

	// Configure mock behaviors
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{modelRegistry}, nil)
	mockStorage.On("ListEndpoint", endpointModelReferenceFilterMatcher("default", "test-registry", "test-model")).
		Return([]v1.Endpoint{}, nil)
	mockModelRegistry.On("Connect").Return(nil)
	mockModelRegistry.On("Disconnect").Return(nil)
	mockModelRegistry.On("DeleteModel", "test-model", v1.LatestVersion).Return(nil)

	// Call the handler function directly
	handlerFunc := deleteModel(deps)
	handlerFunc(c)

	// Verify the results
	assert.Equal(t, http.StatusNoContent, c.Writer.Status())

	// Verify mock expectations
	mockStorage.AssertExpectations(t)
	mockModelRegistry.AssertExpectations(t)
}

func TestDeleteModel_BlockedWhenEndpointReferencesExactVersion(t *testing.T) {
	mockStorage, mockModelRegistry := setupMocks(t)

	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return t.TempDir(), nil
		},
	}

	c, w := createMockContext("default", "test-registry", "test-model", "")
	setVersionQuery(c, "v1.0.0")

	mockStorage.On("ListEndpoint", endpointModelReferenceFilterMatcher("default", "test-registry", "test-model")).
		Return([]v1.Endpoint{endpointWithModel("test-registry", "test-model", "v1.0.0")}, nil)
	mockModelRegistry.On("DeleteModel", "test-model", "v1.0.0").Return(nil).Maybe()

	handlerFunc := deleteModel(deps)
	handlerFunc(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "10131", response["code"])
	assert.Contains(t, response["message"], "cannot delete model 'default/test-registry/test-model:v1.0.0'")
	assert.Contains(t, response["hint"], "1 endpoint(s) still reference this model")

	mockModelRegistry.AssertNotCalled(t, "DeleteModel", "test-model", "v1.0.0")
	mockStorage.AssertExpectations(t)
	mockModelRegistry.AssertExpectations(t)
}

func TestDeleteModel_BlockedWhenEndpointReferencesLatestVersion(t *testing.T) {
	testCases := []struct {
		name            string
		endpointVersion string
	}{
		{
			name:            "latest",
			endpointVersion: v1.LatestVersion,
		},
		{
			name:            "empty",
			endpointVersion: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockStorage, mockModelRegistry := setupMocks(t)

			deps := &Dependencies{
				Storage: mockStorage,
				TempDirFunc: func() (string, error) {
					return t.TempDir(), nil
				},
			}

			c, w := createMockContext("default", "test-registry", "test-model", "")
			setVersionQuery(c, "v1.0.0")

			mockStorage.On("ListEndpoint", endpointModelReferenceFilterMatcher("default", "test-registry", "test-model")).
				Return([]v1.Endpoint{endpointWithModel("test-registry", "test-model", tc.endpointVersion)}, nil)
			mockModelRegistry.On("DeleteModel", "test-model", "v1.0.0").Return(nil).Maybe()

			handlerFunc := deleteModel(deps)
			handlerFunc(c)

			assert.Equal(t, http.StatusBadRequest, w.Code)

			var response map[string]string
			err := json.Unmarshal(w.Body.Bytes(), &response)
			assert.NoError(t, err)
			assert.Equal(t, "10131", response["code"])
			assert.Contains(t, response["hint"], "1 endpoint(s) still reference this model")

			mockModelRegistry.AssertNotCalled(t, "DeleteModel", "test-model", "v1.0.0")
			mockStorage.AssertExpectations(t)
			mockModelRegistry.AssertExpectations(t)
		})
	}
}

func TestDeleteModel_BlockedWhenDeletingLatestAndEndpointReferencesConcreteVersion(t *testing.T) {
	mockStorage, mockModelRegistry := setupMocks(t)

	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return t.TempDir(), nil
		},
	}

	c, w := createMockContext("default", "test-registry", "test-model", "")

	mockStorage.On("ListEndpoint", endpointModelReferenceFilterMatcher("default", "test-registry", "test-model")).
		Return([]v1.Endpoint{endpointWithModel("test-registry", "test-model", "v1.0.0")}, nil)
	mockModelRegistry.On("DeleteModel", "test-model", v1.LatestVersion).Return(nil).Maybe()

	handlerFunc := deleteModel(deps)
	handlerFunc(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "10131", response["code"])
	assert.Contains(t, response["message"], "cannot delete model 'default/test-registry/test-model:latest'")
	assert.Contains(t, response["hint"], "1 endpoint(s) still reference this model")

	mockModelRegistry.AssertNotCalled(t, "DeleteModel", "test-model", v1.LatestVersion)
	mockStorage.AssertExpectations(t)
	mockModelRegistry.AssertExpectations(t)
}

func TestDeleteModel_AllowsUnrelatedEndpointVersion(t *testing.T) {
	mockStorage, mockModelRegistry := setupMocks(t)

	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return t.TempDir(), nil
		},
	}

	c, _ := createMockContext("default", "test-registry", "test-model", "")
	setVersionQuery(c, "v1.0.0")

	modelRegistry := v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: "bentoml",
		},
	}

	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{modelRegistry}, nil)
	mockStorage.On("ListEndpoint", endpointModelReferenceFilterMatcher("default", "test-registry", "test-model")).
		Return([]v1.Endpoint{endpointWithModel("test-registry", "test-model", "v2.0.0")}, nil)
	mockModelRegistry.On("Connect").Return(nil)
	mockModelRegistry.On("Disconnect").Return(nil)
	mockModelRegistry.On("DeleteModel", "test-model", "v1.0.0").Return(nil)

	handlerFunc := deleteModel(deps)
	handlerFunc(c)

	assert.Equal(t, http.StatusNoContent, c.Writer.Status())

	mockStorage.AssertExpectations(t)
	mockModelRegistry.AssertExpectations(t)
}

func TestDeleteModel_ValidationErrorSkipsRegistryDelete(t *testing.T) {
	mockStorage, mockModelRegistry := setupMocks(t)

	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return t.TempDir(), nil
		},
	}

	c, w := createMockContext("default", "test-registry", "test-model", "")

	mockStorage.On("ListEndpoint", endpointModelReferenceFilterMatcher("default", "test-registry", "test-model")).
		Return([]v1.Endpoint{}, errors.New("list endpoint error"))
	mockModelRegistry.On("DeleteModel", "test-model", v1.LatestVersion).Return(nil).Maybe()

	handlerFunc := deleteModel(deps)
	handlerFunc(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["message"], "Failed to validate model deletion")

	mockModelRegistry.AssertNotCalled(t, "DeleteModel", "test-model", v1.LatestVersion)
	mockStorage.AssertExpectations(t)
	mockModelRegistry.AssertExpectations(t)
}

func TestUploadModel_InvalidName(t *testing.T) {
	mockStorage, mockModelRegistry := setupMocks(t)
	deps := &Dependencies{
		Storage: mockStorage,
		TempDirFunc: func() (string, error) {
			return t.TempDir(), nil
		},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	assert.NoError(t, writer.WriteField("name", "Invalid_Name"))
	assert.NoError(t, writer.WriteField("version", "v1.0"))
	part, err := writer.CreateFormFile("model", "model.bentomodel")
	assert.NoError(t, err)
	_, err = part.Write([]byte("model data"))
	assert.NoError(t, err)
	assert.NoError(t, writer.Close())

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/default/model_registries/test-registry/models", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	c.Params = []gin.Param{
		{Key: "workspace", Value: "default"},
		{Key: "registry", Value: "test-registry"},
	}

	uploadModel(deps)(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var response map[string]string
	err = json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Contains(t, response["message"], "Invalid model name")
	assert.Contains(t, response["message"], "model name must be lowercase")

	mockStorage.AssertNotCalled(t, "ListModelRegistry", mock.Anything)
	mockModelRegistry.AssertNotCalled(t, "ImportModel", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}
