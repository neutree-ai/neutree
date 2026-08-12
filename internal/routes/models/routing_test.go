package models

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	"github.com/neutree-ai/neutree/internal/routes"
)

// newRoutedEngine registers the real routes on an engine configured as production
// configures it. These tests must go through the router rather than calling
// handlers: the defect they cover was a request that reached no handler.
func newRoutedEngine(t *testing.T, deps *Dependencies) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	routes.ConfigureEngine(engine)

	authenticated := []gin.HandlerFunc{func(c *gin.Context) { c.Set("user_id", "test-user") }}
	RegisterModelsRoutes(&engine.RouterGroup, authenticated, deps)

	return engine
}

// modelRegistryFixture wires storage and a registry client that record which model
// name the handler was given.
func modelRegistryFixture(t *testing.T, registryType v1.ModelRegistryType) (*Dependencies, *string) {
	t.Helper()

	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: registryType},
	}}, nil)
	mockStorage.On("ListModelAlias", mock.Anything).Return([]v1.ModelAlias{}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)

	var seen string

	mockRegistry.On("GetModelDetail", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			seen, _ = args.Get(0).(string)
		}).Return(&v1.ModelVersion{Name: v1.LatestVersion}, nil)

	mockRegistry.On("GetReadme", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			seen, _ = args.Get(0).(string)
		}).Return(&model_registry.Readme{Content: "# card"}, nil)

	// Permission checks are not what these tests are about.
	mockStorage.On("CallDatabaseFunction", "has_permission", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			result, ok := args.Get(2).(*bool)
			if ok {
				*result = true
			}
		}).Return(nil)

	return &Dependencies{Storage: mockStorage}, &seen
}

// Every model on the hub is named "org/model": the client encodes the slash, the
// router keeps it encoded long enough to match, and the handler gets it decoded.
func TestRouting_PublicModelNameWithASlash(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantModel string
	}{
		{
			name:      "detail",
			path:      "/workspaces/default/model_registries/test-registry/models/Qwen%2FQwen3-8B",
			wantModel: "Qwen/Qwen3-8B",
		},
		{
			name:      "readme",
			path:      "/workspaces/default/model_registries/test-registry/models/Qwen%2FQwen3-8B/readme",
			wantModel: "Qwen/Qwen3-8B",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, seen := modelRegistryFixture(t, v1.HuggingFaceModelRegistryType)
			engine := newRoutedEngine(t, deps)

			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, tt.wantModel, *seen,
				"the handler must receive the name decoded, not the escaped form")
		})
	}
}

// The regression is a framework-level 404 — gin answering before any handler runs.
// Asserting "not 404" alone would also pass if the route were deleted, so this
// pins the old failure too.
func TestRouting_EncodedNameIsNotAFrameworkNotFound(t *testing.T) {
	deps, _ := modelRegistryFixture(t, v1.HuggingFaceModelRegistryType)

	unconfigured := gin.New()
	authenticated := []gin.HandlerFunc{func(c *gin.Context) { c.Set("user_id", "test-user") }}
	RegisterModelsRoutes(&unconfigured.RouterGroup, authenticated, deps)

	path := "/workspaces/default/model_registries/test-registry/models/Qwen%2FQwen3-8B"

	w := httptest.NewRecorder()
	unconfigured.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

	// Without the setting, this is the bug: gin's own 404, no handler, no JSON.
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "404 page not found")

	// With it, the same request is served. Same engine construction as production.
	configured := newRoutedEngine(t, deps)

	w = httptest.NewRecorder()
	configured.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// Private model names are validated to [a-z0-9._-] and never need escaping, so
// their URLs must route exactly as before.
func TestRouting_PrivateModelNamesAreUnaffected(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{
			name: "detail",
			path: "/workspaces/default/model_registries/test-registry/models/qwen2.5-0.5b-instruct",
		},
		{
			name: "readme",
			path: "/workspaces/default/model_registries/test-registry/models/qwen2.5-0.5b-instruct/readme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, seen := modelRegistryFixture(t, v1.BentoMLModelRegistryType)
			engine := newRoutedEngine(t, deps)

			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, "qwen2.5-0.5b-instruct", *seen)
		})
	}
}

// The listing route shares the prefix and must not be shadowed by a model name
// spanning what looks like two segments.
func TestRouting_ListingStillResolves(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: v1.HuggingFaceModelRegistryType},
	}}, nil)
	mockStorage.On("ListModelAlias", mock.Anything).Return([]v1.ModelAlias{}, nil)
	mockStorage.On("CallDatabaseFunction", "has_permission", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			result, ok := args.Get(2).(*bool)
			if ok {
				*result = true
			}
		}).Return(nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("ListModels", mock.Anything).
		Return(&model_registry.ModelPage{Models: []v1.GeneralModel{}}, nil)

	engine := newRoutedEngine(t, &Dependencies{Storage: mockStorage})

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/workspaces/default/model_registries/test-registry/models?search=qwen", nil))

	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// A literal slash is a different URL and matches no route. Left as a 404: making
// the router guess how many trailing segments belong to a name would be ambiguous
// against /readme and /download.
func TestRouting_UnencodedSlashIsStillNotFound(t *testing.T) {
	deps, _ := modelRegistryFixture(t, v1.HuggingFaceModelRegistryType)
	engine := newRoutedEngine(t, deps)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/workspaces/default/model_registries/test-registry/models/Qwen/Qwen3-8B", nil))

	assert.Equal(t, http.StatusNotFound, w.Code)
}
