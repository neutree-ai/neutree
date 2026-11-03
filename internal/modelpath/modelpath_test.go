package modelpath

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	modelregistrymocks "github.com/neutree-ai/neutree/internal/model_registry/mocks"
)

const testMountRoot = "/models-cache"

func TestPathFallsBackToTheModelNameWhenNothingIsPlaced(t *testing.T) {
	for _, tc := range []struct {
		name          string
		modelRegistry *v1.ModelRegistry
	}{
		{name: "no registry"},
		{
			name: "registry without a type",
			modelRegistry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{Type: "unsupported"},
			},
		},
		{
			name: "bentoml served over http",
			modelRegistry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{Type: v1.BentoMLModelRegistryType, Url: "http://bentoml.example"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := Path(&v1.Cluster{}, endpointWithModel("Qwen/Qwen3", "v1"), tc.modelRegistry, testMountRoot)
			require.NoError(t, err)
			require.Equal(t, "Qwen/Qwen3", path)
		})
	}
}

func TestPathUsesTheClusterModelCache(t *testing.T) {
	registry := &v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{Type: v1.HuggingFaceModelRegistryType},
	}

	for _, tc := range []struct {
		name    string
		cluster *v1.Cluster
		want    string
	}{
		{
			name: "first configured cache",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{
					Config: &v1.ClusterConfig{
						ModelCaches: []v1.ModelCache{{Name: "cache-a"}, {Name: "cache-b"}},
					},
				},
			},
			want: "/models-cache/cache-a/Qwen/Qwen3/v1.2.3",
		},
		{
			name:    "default cache when none is configured",
			cluster: &v1.Cluster{},
			want:    "/models-cache/default/Qwen/Qwen3/v1.2.3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := Path(tc.cluster, endpointWithModel("Qwen/Qwen3", "v1.2.3"), registry, testMountRoot)
			require.NoError(t, err)
			require.Equal(t, tc.want, path)
		})
	}
}

// A hub registry has no stored artifact to look up, so an empty or "latest"
// version selects the repository's default branch, which the downloader places
// directly under the cache with no version directory.
func TestPathOmitsTheVersionDirectoryForAHubDefaultBranch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		registry v1.ModelRegistryType
		version  string
	}{
		{name: "hugging face without a version", registry: v1.HuggingFaceModelRegistryType},
		{name: "model scope latest", registry: v1.ModelScopeModelRegistryType, version: v1.LatestVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := Path(&v1.Cluster{}, endpointWithModel("Qwen/Qwen3", tc.version), &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{Type: tc.registry},
			}, testMountRoot)

			require.NoError(t, err)
			require.Equal(t, "/models-cache/default/Qwen/Qwen3", path)
		})
	}
}

func TestPathUsesAPinnedBentoMLVersion(t *testing.T) {
	path, err := Path(&v1.Cluster{}, endpointWithModel("iris", "v3"), &v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{Type: v1.BentoMLModelRegistryType, Url: "nfs://bentoml.example/models"},
	}, testMountRoot)

	require.NoError(t, err)
	require.Equal(t, "/models-cache/default/iris/v3", path)
}

func TestRealVersionResolvesABentoMLArtifact(t *testing.T) {
	for _, tc := range []struct {
		name         string
		setupMock    func(*modelregistrymocks.MockModelRegistry)
		inputVersion string
		want         string
		wantErr      bool
	}{
		{
			name: "latest resolves to the stored version",
			setupMock: func(modelregistry *modelregistrymocks.MockModelRegistry) {
				modelregistry.On("GetModelVersion", "test", "latest").Return(&v1.ModelVersion{Name: "v1.0.0"}, nil)
			},
			inputVersion: "latest",
			want:         "v1.0.0",
		},
		{
			name: "empty version resolves to the stored version",
			setupMock: func(modelregistry *modelregistrymocks.MockModelRegistry) {
				modelregistry.On("GetModelVersion", "test", "").Return(&v1.ModelVersion{Name: "v1.0.0"}, nil)
			},
			inputVersion: "",
			want:         "v1.0.0",
		},
		{
			name: "a pinned version is not looked up",
			setupMock: func(modelregistry *modelregistrymocks.MockModelRegistry) {
			},
			inputVersion: "v2.0.0",
			want:         "v2.0.0",
		},
		{
			name: "lookup failure is reported",
			setupMock: func(modelregistry *modelregistrymocks.MockModelRegistry) {
				modelregistry.On("GetModelVersion", "test", "latest").Return(nil, assert.AnError)
			},
			inputVersion: "latest",
			wantErr:      true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockRegistry := &modelregistrymocks.MockModelRegistry{}
			tc.setupMock(mockRegistry)

			previous := model_registry.NewModelRegistry
			model_registry.NewModelRegistry = func(*v1.ModelRegistry) (model_registry.ModelRegistry, error) {
				return mockRegistry, nil
			}
			t.Cleanup(func() { model_registry.NewModelRegistry = previous })

			got, err := RealVersion(&v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{Type: v1.BentoMLModelRegistryType},
			}, "test", tc.inputVersion)

			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}

			mockRegistry.AssertExpectations(t)
		})
	}
}

func TestRealVersionPassesHubRevisionsThrough(t *testing.T) {
	// ModelScope's "version" is a git revision, so there is nothing to look up.
	// Its "latest" still cannot be passed through: the hub's default branch is
	// "master", and a wrong revision is not refused — it answers HTTP 200 with an
	// empty file list. "" makes the downloader omit the Revision parameter and
	// let the hub resolve its own default.
	for _, tc := range []struct {
		name     string
		registry v1.ModelRegistryType
		version  string
		want     string
	}{
		{name: "hugging face default branch", registry: v1.HuggingFaceModelRegistryType, version: "", want: ""},
		{name: "hugging face revision", registry: v1.HuggingFaceModelRegistryType, version: "latest", want: "latest"},
		{name: "model scope latest", registry: v1.ModelScopeModelRegistryType, version: "latest", want: ""},
		{name: "model scope default branch", registry: v1.ModelScopeModelRegistryType, version: "", want: ""},
		{name: "model scope explicit revision", registry: v1.ModelScopeModelRegistryType, version: "v1.0.0", want: "v1.0.0"},
		{name: "model scope master", registry: v1.ModelScopeModelRegistryType, version: "master", want: "master"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RealVersion(&v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{Type: tc.registry},
			}, "Qwen/Qwen3-8B", tc.version)

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestRealVersionRejectsAnUnusableRegistry(t *testing.T) {
	for _, tc := range []struct {
		name          string
		modelRegistry *v1.ModelRegistry
		wantErr       string
	}{
		{
			name:          "nil registry",
			modelRegistry: nil,
			wantErr:       "model registry cannot be nil",
		},
		{
			name:          "nil spec",
			modelRegistry: &v1.ModelRegistry{},
			wantErr:       "model registry spec cannot be nil",
		},
		{
			name: "unsupported type",
			modelRegistry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{Type: "unsupported_type"},
			},
			wantErr: "unsupported model registry type: unsupported_type",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RealVersion(tc.modelRegistry, "test", "latest")
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func endpointWithModel(name, version string) *v1.Endpoint {
	return &v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Model: &v1.ModelSpec{Name: name, Version: version},
		},
	}
}
