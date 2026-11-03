// Package modelpath computes the path an engine reads its model from inside
// its container.
package modelpath

import (
	"fmt"
	"net/url"
	"path/filepath"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	"github.com/neutree-ai/neutree/internal/util"
)

// Path returns the path the engine reads the model from inside its container.
//
// The deploy templates pass it to the engine as its model argument, so a
// process running in the same container -- a benchmark, for instance -- reads
// the very files the serving engine serves. mountRoot is where the platform
// mounts the cluster model cache.
//
// A registry the orchestrator does not place from leaves the model at the bare
// name the endpoint spec carries.
func Path(cluster *v1.Cluster, endpoint *v1.Endpoint, modelRegistry *v1.ModelRegistry, mountRoot string) (string, error) {
	path, _, err := Resolve(cluster, endpoint, modelRegistry, mountRoot)
	return path, err
}

// Resolve returns the container path and the model version that path was built
// from. Callers that also need the version -- the deploy variables carry both
// path and version -- resolve it once here instead of reading the registry
// again.
func Resolve(cluster *v1.Cluster, endpoint *v1.Endpoint, modelRegistry *v1.ModelRegistry, mountRoot string) (string, string, error) {
	if cluster == nil || endpoint == nil || endpoint.Spec == nil || endpoint.Spec.Model == nil {
		return "", "", fmt.Errorf("cluster, endpoint, and endpoint model are required")
	}

	modelVersion := endpoint.Spec.Model.Version

	if placedIntoModelCache(modelRegistry) {
		var err error

		modelVersion, err = RealVersion(modelRegistry, endpoint.Spec.Model.Name, modelVersion)
		if err != nil {
			return "", "", fmt.Errorf("get deployed model real version for model %s: %w", endpoint.Spec.Model.Name, err)
		}
	}

	path, err := PathWithVersion(cluster, endpoint, modelRegistry, mountRoot, modelVersion)
	if err != nil {
		return "", "", err
	}

	return path, modelVersion, nil
}

// PathWithVersion returns the container path for a model version the caller
// already resolved. The control plane reads a registry's version reference
// from its own mount; a caller without that mount has to ask the control plane
// and place the model here.
func PathWithVersion(cluster *v1.Cluster, endpoint *v1.Endpoint, modelRegistry *v1.ModelRegistry, mountRoot, modelVersion string) (string, error) {
	if cluster == nil || endpoint == nil || endpoint.Spec == nil || endpoint.Spec.Model == nil {
		return "", fmt.Errorf("cluster, endpoint, and endpoint model are required")
	}

	modelName := endpoint.Spec.Model.Name

	if !placedIntoModelCache(modelRegistry) {
		return modelName, nil
	}

	modelCacheRelativePath, err := clusterModelCacheRelativePath(cluster)
	if err != nil {
		return "", err
	}

	return filepath.Join(mountRoot, modelCacheRelativePath, modelName, modelVersion), nil
}

// NormalizeVersion returns the version the deployment places the model under,
// without reading registry storage. It applies the same normalization as the
// deploy path, so a caller that builds the model path itself does not spell a
// version reference differently from the engine. A reference that needs
// storage reports itself through RealVersionNeedsStorage and has to be
// resolved before calling this.
func NormalizeVersion(modelRegistry *v1.ModelRegistry, modelName, modelVersion string) (string, error) {
	if !placedIntoModelCache(modelRegistry) {
		return modelVersion, nil
	}

	return RealVersion(modelRegistry, modelName, modelVersion)
}

func clusterModelCacheRelativePath(cluster *v1.Cluster) (string, error) {
	modelCaches, err := util.GetClusterModelCache(*cluster)
	if err != nil {
		return "", fmt.Errorf("get cluster model cache: %w", err)
	}

	// TODO: Now we only use the first model cache for simplicity, In the future, we may support specific model cache.
	if len(modelCaches) > 0 {
		return modelCaches[0].Name, nil
	}

	return v1.DefaultModelCacheRelativePath, nil
}

// RealVersionNeedsStorage reports whether RealVersion reads the registry's
// storage to resolve a version reference. A caller whose host does not mount
// the registry cannot answer that lookup and has to ask the control plane.
func RealVersionNeedsStorage(modelRegistry *v1.ModelRegistry, version string) bool {
	return RegistryType(modelRegistry) == v1.BentoMLModelRegistryType &&
		(version == "" || version == v1.LatestVersion)
}

// placedIntoModelCache reports whether the orchestrator downloads the model
// into the cluster model cache. A BentoML registry is only mounted from NFS;
// the other BentoML forms keep the model where the registry serves it.
func placedIntoModelCache(modelRegistry *v1.ModelRegistry) bool {
	switch RegistryType(modelRegistry) {
	case v1.BentoMLModelRegistryType:
		registryURL, _ := url.Parse(modelRegistry.Spec.Url) // nolint: errcheck

		return registryURL != nil && registryURL.Scheme == v1.BentoMLModelRegistryConnectTypeNFS
	case v1.HuggingFaceModelRegistryType, v1.ModelScopeModelRegistryType:
		return true
	default:
		return false
	}
}

// RegistryType returns the registry type, or "" when the endpoint deploys
// without a registry.
func RegistryType(modelRegistry *v1.ModelRegistry) v1.ModelRegistryType {
	if modelRegistry == nil || modelRegistry.Spec == nil {
		return ""
	}

	return modelRegistry.Spec.Type
}

// RealVersion resolves the model version actually placed on disk. Hub
// registries carry a git revision rather than a stored artifact version, so
// their version is passed through unchanged.
func RealVersion(modelRegistry *v1.ModelRegistry, modelName, modelVersion string) (string, error) {
	if modelRegistry == nil {
		return "", fmt.Errorf("model registry cannot be nil")
	}

	if modelRegistry.Spec == nil {
		return "", fmt.Errorf("model registry spec cannot be nil")
	}

	switch modelRegistry.Spec.Type {
	case v1.HuggingFaceModelRegistryType:
		// For HuggingFace, return the version as-is (including empty string).
		// Empty string will be converted to None by the Python downloader,
		// which causes huggingface_hub to use the repository's default branch (main/master/etc).
		return modelVersion, nil
	case v1.ModelScopeModelRegistryType:
		// Same shape as Hugging Face — the "version" is a git revision, not a
		// stored artifact version, so there is nothing to look up.
		//
		// "latest" is normalised away rather than passed through. ModelScope's
		// default branch is "master", not "main", and a wrong revision is not
		// refused by the hub: it answers HTTP 200 with an empty file list. The
		// downloader therefore omits the Revision parameter entirely for the
		// default, letting the hub resolve the repository's own default branch,
		// and "" is how that intent is spelled. This matches modelScope.revision()
		// in internal/model_registry/model_scope.go.
		if modelVersion == v1.LatestVersion {
			return "", nil
		}

		return modelVersion, nil
	case v1.BentoMLModelRegistryType:
		if modelVersion != "" && modelVersion != v1.LatestVersion {
			return modelVersion, nil
		}

		registryManager, err := model_registry.NewModelRegistry(modelRegistry)
		if err != nil {
			return "", fmt.Errorf("failed to create model registry manager for model registry %s: %w", modelRegistry.Metadata.Name, err)
		}

		latestModelVersionInfo, err := registryManager.GetModelVersion(modelName, modelVersion)
		if err != nil {
			return "", fmt.Errorf("failed to get latest model version for %s: %w", modelName, err)
		}

		return latestModelVersionInfo.Name, nil
	default:
		return "", fmt.Errorf("unsupported model registry type: %s", modelRegistry.Spec.Type)
	}
}
