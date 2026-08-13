package models

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
)

func registryRow(registryType v1.ModelRegistryType) []v1.ModelRegistry {
	return []v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: registryType, Url: "https://huggingface.co"},
	}}
}

// Paging through a public hub's results should not re-query it for every page
// the user steps back and forth over.
func TestListModels_PublicRegistryAnswersFromCache(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return(registryRow(v1.HuggingFaceModelRegistryType), nil)
	mockStorage.On("ListModelAlias", mock.Anything).Return([]v1.ModelAlias{}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("ListModels", mock.Anything).Return(&model_registry.ModelPage{
		Models: []v1.GeneralModel{storedModel("qwen/qwen3", v1.LatestVersion)},
	}, nil)

	deps := &Dependencies{Storage: mockStorage, QueryCache: model_registry.NewQueryCache(0)}

	for i := 0; i < 3; i++ {
		c, w := newListContext(t, "search=qwen&limit=2")
		listModels(deps)(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}

	mockRegistry.AssertNumberOfCalls(t, "ListModels", 1)
}

// A private registry is a local tree: cheap to list, and a model pushed a second
// ago has to appear immediately.
func TestListModels_PrivateRegistryIsReadEveryTime(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return(registryRow(v1.BentoMLModelRegistryType), nil)
	mockStorage.On("ListModelAlias", mock.Anything).Return([]v1.ModelAlias{}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("ListModels", mock.Anything).Return(&model_registry.ModelPage{
		Models: []v1.GeneralModel{storedModel("local", "v1")},
	}, nil)

	deps := &Dependencies{Storage: mockStorage, QueryCache: model_registry.NewQueryCache(0)}

	for i := 0; i < 3; i++ {
		c, w := newListContext(t, "")
		listModels(deps)(c)
		require.Equal(t, http.StatusOK, w.Code)
	}

	mockRegistry.AssertNumberOfCalls(t, "ListModels", 3)
}

// The listing decorates the models it gets with aliases. A cached page handed
// back by reference would collect one request's decorations and show them to the
// next.
func TestListModels_CachedPageIsNotDecoratedInPlace(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return(registryRow(v1.HuggingFaceModelRegistryType), nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)

	page := &model_registry.ModelPage{Models: []v1.GeneralModel{storedModel("qwen/qwen3", v1.LatestVersion)}}
	mockRegistry.On("ListModels", mock.Anything).Return(page, nil)

	deps := &Dependencies{Storage: mockStorage, QueryCache: model_registry.NewQueryCache(0)}

	mockStorage.On("ListModelAlias", mock.Anything).Return([]v1.ModelAlias{{
		ID:              1,
		ModelRegistryID: 7,
		ModelName:       "qwen/qwen3",
		ModelVersion:    v1.LatestVersion,
		Alias:           "renamed once",
	}}, nil).Once()

	c, w := newListContext(t, "")
	listModels(deps)(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// Second time round the alias is gone from the table; the cached page must
	// not still be carrying it.
	mockStorage.On("ListModelAlias", mock.Anything).Return([]v1.ModelAlias{}, nil)

	c, w = newListContext(t, "")
	listModels(deps)(c)
	require.Equal(t, http.StatusOK, w.Code)

	assert.NotContains(t, w.Body.String(), "renamed once")
}
