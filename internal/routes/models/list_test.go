package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
)

func newListContext(t *testing.T, rawQuery string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	url := "/api/v1/workspaces/default/model_registries/test-registry/models"
	if rawQuery != "" {
		url += "?" + rawQuery
	}

	c.Request = httptest.NewRequest(http.MethodGet, url, nil)
	c.Params = []gin.Param{
		{Key: "workspace", Value: "default"},
		{Key: "registry", Value: "test-registry"},
	}

	return c, w
}

// The total travels in a header and the body stays a bare array: two existing
// callers consume the response as an array, and an envelope would break them
// across repositories for the sake of one number.
func TestListModels_ReportsTotalInContentRange(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		returned   []v1.GeneralModel
		total      *int
		wantOffset int
		wantLimit  int
		wantHeader string
	}{
		{
			name:       "whole listing",
			returned:   []v1.GeneralModel{storedModel("a", "v1"), storedModel("b", "v1")},
			total:      model_registry.KnownTotal(2),
			wantHeader: "0-1/2",
		},
		{
			name:       "second page",
			query:      "offset=2&limit=2",
			returned:   []v1.GeneralModel{storedModel("c", "v1")},
			total:      model_registry.KnownTotal(5),
			wantOffset: 2,
			wantLimit:  2,
			wantHeader: "2-2/5",
		},
		{
			name:       "offset past the end",
			query:      "offset=50",
			returned:   []v1.GeneralModel{},
			total:      model_registry.KnownTotal(5),
			wantOffset: 50,
			wantHeader: "*/5",
		},
		{
			name:       "empty registry",
			returned:   []v1.GeneralModel{},
			total:      model_registry.KnownTotal(0),
			wantHeader: "*/0",
		},
		{
			// A registry that cannot count what matched says so, rather than
			// passing off the page size as the total.
			name:       "registry cannot count its contents",
			returned:   []v1.GeneralModel{storedModel("a", "v1"), storedModel("b", "v1")},
			total:      nil,
			wantHeader: "0-1/*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage, mockRegistry := setupMocks(t)
			mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
				ID:       7,
				Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
				Spec:     &v1.ModelRegistrySpec{Type: v1.BentoMLModelRegistryType},
			}}, nil)
			mockStorage.On("ListModelAlias", mock.Anything).Return([]v1.ModelAlias{}, nil)
			mockRegistry.On("Connect").Return(nil)
			mockRegistry.On("Disconnect").Return(nil)
			mockRegistry.On("ListModels", mock.MatchedBy(func(option model_registry.ListOption) bool {
				return option.Offset == tt.wantOffset && option.Limit == tt.wantLimit
			})).Return(&model_registry.ModelPage{Models: tt.returned, Total: tt.total}, nil)

			c, w := newListContext(t, tt.query)
			listModels(&Dependencies{Storage: mockStorage})(c)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, tt.wantHeader, w.Header().Get("Content-Range"))

			var body []v1.GeneralModel
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Len(t, body, len(tt.returned))

			mockStorage.AssertExpectations(t)
			mockRegistry.AssertExpectations(t)
		})
	}
}

func TestListModels_RejectsUnusablePaging(t *testing.T) {
	for _, query := range []string{"limit=abc", "offset=-1", "offset=x"} {
		t.Run(query, func(t *testing.T) {
			mockStorage, _ := setupMocks(t)

			c, w := newListContext(t, query)
			listModels(&Dependencies{Storage: mockStorage})(c)

			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

// A listing surfaces the alias next to the version it belongs to, and an alias
// whose model is no longer in the registry stays invisible.
func TestListModels_AttachesAliases(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: v1.BentoMLModelRegistryType},
	}}, nil)
	mockStorage.On("ListModelAlias", mock.Anything).Return([]v1.ModelAlias{
		{ID: 1, ModelRegistryID: 7, ModelName: "qwen3", ModelVersion: "v1", Alias: "Qwen3 Chat"},
		{ID: 2, ModelRegistryID: 7, ModelName: "removed-out-of-band", ModelVersion: "v1", Alias: "Ghost"},
	}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("ListModels", mock.Anything).Return(&model_registry.ModelPage{
		Models: []v1.GeneralModel{storedModel("qwen3", "v1", "v2")},
		Total:  model_registry.KnownTotal(1),
	}, nil)

	c, w := newListContext(t, "")
	listModels(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusOK, w.Code)

	var body []v1.GeneralModel
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body, 1)
	require.Len(t, body[0].Versions, 2)
	assert.Equal(t, "Qwen3 Chat", body[0].Versions[0].Alias)
	assert.Empty(t, body[0].Versions[1].Alias)
	assert.NotContains(t, w.Body.String(), "Ghost")
}

func TestContentRange(t *testing.T) {
	assert.Equal(t, "0-9/100", contentRange(0, 10, model_registry.KnownTotal(100)))
	assert.Equal(t, "10-19/100", contentRange(10, 10, model_registry.KnownTotal(100)))
	assert.Equal(t, "*/100", contentRange(200, 0, model_registry.KnownTotal(100)))
	assert.Equal(t, "*/0", contentRange(0, 0, model_registry.KnownTotal(0)))
	// An unknown total is reported as unknown, not as zero.
	assert.Equal(t, "0-9/*", contentRange(0, 10, nil))
	assert.Equal(t, "*/*", contentRange(0, 0, nil))
}

// A registry that refuses to page from an offset gets a plain refusal, not a
// server error and not a silent first page.
func TestListModels_OffsetRefusedByRegistry(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       8,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: v1.HuggingFaceModelRegistryType},
	}}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("ListModels", mock.Anything).
		Return(nil, errors.Wrap(model_registry.ErrNotSupported, "cannot list from an offset"))

	c, w := newListContext(t, "offset=10&limit=5")
	listModels(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "cannot list models this way")
	assert.Empty(t, w.Header().Get("Content-Range"))
}
