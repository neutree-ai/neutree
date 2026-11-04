package util

import (
	"fmt"
	"path/filepath"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ModelCachePath returns the path an engine reads a model placed in the
// cluster's model cache from. mountRoot is where that cache is mounted in the
// engine's container; the relative part is chosen the same way the deploy path
// chooses it.
func ModelCachePath(cluster v1.Cluster, modelName, modelVersion, mountRoot string) (string, error) {
	modelCaches, err := GetClusterModelCache(cluster)
	if err != nil {
		return "", fmt.Errorf("get cluster model cache: %w", err)
	}

	// TODO: Now we only use the first model cache for simplicity, In the future, we may support specific model cache.
	modelCacheRelativePath := v1.DefaultModelCacheRelativePath
	if len(modelCaches) > 0 {
		modelCacheRelativePath = modelCaches[0].Name
	}

	return filepath.Join(mountRoot, modelCacheRelativePath, modelName, modelVersion), nil
}
