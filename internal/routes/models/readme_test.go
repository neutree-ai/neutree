package models

import (
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

func newReadmeContext(t *testing.T, rawQuery string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	url := "/api/v1/workspaces/default/model_registries/test-registry/models/qwen3/readme"
	if rawQuery != "" {
		url += "?" + rawQuery
	}

	c.Request = httptest.NewRequest(http.MethodGet, url, nil)
	c.Params = []gin.Param{
		{Key: "workspace", Value: "default"},
		{Key: "registry", Value: "test-registry"},
		{Key: "model", Value: "qwen3"},
	}

	return c, w
}

func TestGetModelReadme_ServesMarkdownVerbatim(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: v1.BentoMLModelRegistryType},
	}}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("GetReadme", "qwen3", "v1").
		Return(&model_registry.Readme{Content: "# Qwen3\n\n<script>alert(1)</script>\n"}, nil)

	c, w := newReadmeContext(t, "version=v1")
	getModelReadme(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	// Markdown, byte for byte, and no rendering: a model card is content from
	// outside this system, and turning it into HTML here would run it in every
	// client that displays the result.
	assert.Equal(t, "# Qwen3\n\n<script>alert(1)</script>\n", w.Body.String())
	assert.Equal(t, readmeContentType, w.Header().Get("Content-Type"))
	assert.Empty(t, w.Header().Get(readmeTruncatedHeader))

	mockStorage.AssertExpectations(t)
	mockRegistry.AssertExpectations(t)
}

func TestGetModelReadme_MarksTruncation(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: v1.BentoMLModelRegistryType},
	}}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("GetReadme", "qwen3", v1.LatestVersion).
		Return(&model_registry.Readme{Content: "# Qwen3", Truncated: true}, nil)

	c, w := newReadmeContext(t, "")
	getModelReadme(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusOK, w.Code)
	// A document that ends abruptly has to be distinguishable from one written
	// that way.
	assert.Equal(t, "true", w.Header().Get(readmeTruncatedHeader))
}

// The three ways a card can fail to arrive need three different answers: one is
// permanent for this registry kind, one is a fact about this model, and one is
// worth retrying.
func TestGetModelReadme_DistinguishesFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantReason string
	}{
		{
			name:       "the model has no card",
			err:        errors.Wrap(model_registry.ErrNotFound, "model qwen3:v1 has no README.md"),
			wantStatus: http.StatusNotFound,
			wantReason: reasonNotFound,
		},
		{
			name:       "the registry does not serve cards",
			err:        errors.Wrap(model_registry.ErrNotSupported, "operation not supported"),
			wantStatus: http.StatusBadRequest,
			wantReason: reasonNotSupported,
		},
		{
			name:       "the registry could not be asked",
			err:        errors.New("502 bad gateway"),
			wantStatus: http.StatusInternalServerError,
			wantReason: reasonUnavailable,
		},
		{
			// Fixed by giving the registry a token, not by retrying and not by
			// changing the request.
			name:       "the registry rejected our credentials",
			err:        errors.Wrap(model_registry.ErrUnauthorized, "a token is required"),
			wantStatus: http.StatusBadRequest,
			wantReason: reasonUnauthorized,
		},
		{
			// Nothing is wrong; come back later. Passed through as itself so a
			// client does not have to parse prose to know that.
			name:       "the registry is throttling us",
			err:        errors.Wrap(model_registry.ErrRateLimited, "429 Too Many Requests"),
			wantStatus: http.StatusTooManyRequests,
			wantReason: reasonRateLimited,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage, mockRegistry := setupMocks(t)
			mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
				ID:       7,
				Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
				Spec:     &v1.ModelRegistrySpec{Type: v1.HuggingFaceModelRegistryType},
			}}, nil)
			mockRegistry.On("Connect").Return(nil)
			mockRegistry.On("Disconnect").Return(nil)
			mockRegistry.On("GetReadme", "qwen3", v1.LatestVersion).
				Return((*model_registry.Readme)(nil), tt.err)

			c, w := newReadmeContext(t, "")
			getModelReadme(&Dependencies{Storage: mockStorage})(c)

			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Contains(t, w.Body.String(), tt.wantReason)
		})
	}
}
