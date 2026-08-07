package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
)

func newRetryContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Request = httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/default/model_registries/public-hugging-face/retry_connection", nil)
	c.Params = []gin.Param{
		{Key: "workspace", Value: "default"},
		{Key: "registry", Value: "public-hugging-face"},
	}

	return c, w
}

func failedPublicRegistry() v1.ModelRegistry {
	return v1.ModelRegistry{
		ID:       7,
		Metadata: &v1.Metadata{Name: "public-hugging-face", Workspace: "default"},
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
			Url:  "https://huggingface.co",
		},
		Status: &v1.ModelRegistryStatus{
			Phase:              v1.ModelRegistryPhaseFAILED,
			ErrorMessage:       "invalid Hugging Face API token",
			LastTransitionTime: "2026-08-01T00:00:00Z",
			Stats:              &v1.ModelRegistryStats{ModelCount: 0},
		},
	}
}

func TestRetryConnection_RecordsASuccessfulCheck(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{failedPublicRegistry()}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("HealthyCheck").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)

	var written *v1.ModelRegistryStatus

	mockStorage.On("UpdateModelRegistry", "7", mock.Anything).Run(func(args mock.Arguments) {
		obj, _ := args.Get(1).(*v1.ModelRegistry)
		written = obj.Status
	}).Return(nil)

	c, w := newRetryContext(t)
	retryConnection(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body retryConnectionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, v1.ModelRegistryPhaseCONNECTED, body.Phase)
	assert.Empty(t, body.ErrorMessage)
	assert.NotEmpty(t, body.LastCheckedAt)

	require.NotNil(t, written)
	assert.Equal(t, v1.ModelRegistryPhaseCONNECTED, written.Phase)
	// Failed to Connected is a real transition, so this one does move.
	assert.NotEqual(t, "2026-08-01T00:00:00Z", written.LastTransitionTime)
	// The counters are not this path's to recompute, and PostgREST would null
	// them if they were left out.
	assert.NotNil(t, written.Stats)

	mockStorage.AssertExpectations(t)
	mockRegistry.AssertExpectations(t)
}

// The check ran and produced an answer, so the request succeeded. The answer is
// the payload.
func TestRetryConnection_ReportsAFailedCheckAsData(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{failedPublicRegistry()}, nil)
	mockRegistry.On("Connect").Return(assert.AnError)
	mockStorage.On("UpdateModelRegistry", "7", mock.Anything).Return(nil)

	c, w := newRetryContext(t)
	retryConnection(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusOK, w.Code)

	var body retryConnectionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, v1.ModelRegistryPhaseFAILED, body.Phase)
	assert.NotEmpty(t, body.ErrorMessage)
	assert.NotEmpty(t, body.LastCheckedAt)

	mockRegistry.AssertExpectations(t)
}

// This is the part nothing else does. Without it the status would go green while
// the listing beside it replayed results from before the fix, which reads as the
// retry not having worked.
func TestRetryConnection_DropsTheCachedResults(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	registry := failedPublicRegistry()
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{registry}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("HealthyCheck").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("ListModels", mock.Anything).
		Return(&model_registry.ModelPage{Models: []v1.GeneralModel{{Name: "qwen/qwen3"}}}, nil)
	mockStorage.On("UpdateModelRegistry", "7", mock.Anything).Return(nil)

	cache := model_registry.NewQueryCache(0)
	deps := &Dependencies{Storage: mockStorage, QueryCache: cache}

	// Prime the cache, then confirm it is being used before asserting that the
	// retry clears it — an empty cache would pass the final assertion for the
	// wrong reason.
	_, _, err := cache.ListModels(&registry, mockRegistry, model_registry.ListOption{Search: "qwen"})
	require.NoError(t, err)
	_, _, err = cache.ListModels(&registry, mockRegistry, model_registry.ListOption{Search: "qwen"})
	require.NoError(t, err)
	mockRegistry.AssertNumberOfCalls(t, "ListModels", 1)

	c, _ := newRetryContext(t)
	retryConnection(deps)(c)

	_, _, err = cache.ListModels(&registry, mockRegistry, model_registry.ListOption{Search: "qwen"})
	require.NoError(t, err)
	mockRegistry.AssertNumberOfCalls(t, "ListModels", 2)
}

// A registry that cannot be recorded has still been checked, and the caller is
// owed the answer.
func TestRetryConnection_AnswersEvenIfTheStatusCannotBeStored(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{failedPublicRegistry()}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("HealthyCheck").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockStorage.On("UpdateModelRegistry", "7", mock.Anything).Return(assert.AnError)

	c, w := newRetryContext(t)
	retryConnection(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusOK, w.Code)

	var body retryConnectionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, v1.ModelRegistryPhaseCONNECTED, body.Phase)
}
