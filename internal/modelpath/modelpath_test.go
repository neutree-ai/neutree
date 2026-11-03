package modelpath

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
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

func endpointWithModel(name, version string) *v1.Endpoint {
	return &v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Model: &v1.ModelSpec{Name: name, Version: version},
		},
	}
}
