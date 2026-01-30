package orchestrator

import (
	"maps"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/internal/util"
)

type DeploymentManifestVariables struct {
	EndpointName    string
	Namespace       string
	ClusterName     string
	Workspace       string
	EngineName      string
	EngineVersion   string
	ImagePrefix     string
	ImageRepo       string
	ImageTag        string
	ImagePullSecret string
	ModelArgs       map[string]interface{}
	EngineArgs      map[string]interface{}
	Resources       map[string]string
	Env             map[string]string
	Volumes         []corev1.Volume
	VolumeMounts    []corev1.VolumeMount
	RoutingLogic    string
	Replicas        int32
	NodeSelector    map[string]string
	NeutreeVersion  string
}

func buildDeploymentObjects(deployTemplate string, renderVars DeploymentManifestVariables) (*unstructured.UnstructuredList, error) {
	return util.RenderKubernetesManifest(deployTemplate, renderVars)
}

// setBasicVariables initializes the basic deployment variables
func (k *kubernetesOrchestrator) setBasicVariables(data *DeploymentManifestVariables, endpoint *v1.Endpoint, deployedCluster *v1.Cluster, engine *v1.Engine) {
	data.EndpointName = endpoint.Metadata.Name
	data.Namespace = util.ClusterNamespace(deployedCluster)
	data.ClusterName = deployedCluster.Metadata.Name
	data.Workspace = deployedCluster.Metadata.Workspace
	data.EngineName = engine.Metadata.Name
	data.EngineVersion = endpoint.Spec.Engine.Version
	data.ImagePullSecret = cluster.ImagePullSecretName
	data.Replicas = int32(*endpoint.Spec.Replicas.Num)
	data.RoutingLogic = "roundrobin"
	data.NeutreeVersion = deployedCluster.Spec.Version
}

// setDeployImageVariables sets the container image repository and tag for deployment
func (k *kubernetesOrchestrator) setDeployImageVariables(data *DeploymentManifestVariables,
	endpoint *v1.Endpoint, engine *v1.Engine, imageRegistry *v1.ImageRegistry) error {
	imagePrefix, err := util.GetImagePrefix(imageRegistry)
	if err != nil {
		return errors.Wrapf(err, "failed to get image prefix for image registry %s", imageRegistry.Metadata.WorkspaceName())
	}

	data.ImagePrefix = imagePrefix

	acceleratorType := endpoint.Spec.Resources.GetAcceleratorType()

	imageName, imageTag, err := k.getImageForAccelerator(engine, endpoint.Spec.Engine.Version, acceleratorType)
	if err != nil {
		return errors.Wrapf(err, "failed to get image for accelerator %s", acceleratorType)
	}

	data.ImageRepo = imageName
	data.ImageTag = imageTag

	return nil
}

// setRoutingLogic sets the routing logic from deployment options
func (k *kubernetesOrchestrator) setRoutingLogic(data *DeploymentManifestVariables, endpoint *v1.Endpoint) {
	if endpoint.Spec.DeploymentOptions != nil && endpoint.Spec.DeploymentOptions["scheduler"] != nil {
		scheduleConfig, ok := endpoint.Spec.DeploymentOptions["scheduler"].(map[string]interface{})
		if ok {
			if logic, exists := scheduleConfig["type"].(string); exists {
				data.RoutingLogic = logic
			}
		}
	}
}

// setEngineDefaultArgs sets default arguments for specific engines
func (k *kubernetesOrchestrator) setEngineDefaultArgs(data *DeploymentManifestVariables, engine *v1.Engine) {
	switch engine.Metadata.Name { //nolint:gocritic
	case "llama-cpp":
		// Set default parameters for llama-cpp engine
		if _, exists := data.EngineArgs["interrupt_requests"]; !exists {
			data.EngineArgs["interrupt_requests"] = "false"
		}
	}
}

// setEngineArgs sets engine arguments from endpoint variables
func (k *kubernetesOrchestrator) setEngineArgs(data *DeploymentManifestVariables, endpoint *v1.Endpoint, engine *v1.Engine) {
	// Set engine-specific default arguments first
	k.setEngineDefaultArgs(data, engine)

	// Then apply user-provided arguments (can override defaults)
	if endpoint.Spec.Variables != nil {
		if v, ok := endpoint.Spec.Variables["engine_args"].(map[string]interface{}); ok {
			maps.Copy(data.EngineArgs, v)
		}
	}
}

// setResourceVariables sets resource specifications and node selector
func (k *kubernetesOrchestrator) setResourceVariables(data *DeploymentManifestVariables, endpoint *v1.Endpoint) error {
	resourceSpec, err := convertToKubernetes(k.acceleratorMgr, endpoint.Spec.Resources)
	if err != nil {
		return errors.Wrapf(err, "failed to convert resources for endpoint %s", endpoint.Metadata.Name)
	}

	if resourceSpec == nil {
		return nil
	}

	if resourceSpec.Requests != nil {
		maps.Copy(data.Resources, resourceSpec.Requests)
	}

	if resourceSpec.NodeSelector != nil {
		maps.Copy(data.NodeSelector, resourceSpec.NodeSelector)
	}

	if resourceSpec.Env != nil {
		maps.Copy(data.Env, resourceSpec.Env)
	}

	return nil
}

// setEnvironmentVariables initializes environment variables from endpoint spec
func (k *kubernetesOrchestrator) setEnvironmentVariables(data *DeploymentManifestVariables, endpoint *v1.Endpoint) {
	if endpoint.Spec.Env != nil {
		maps.Copy(data.Env, endpoint.Spec.Env)
	}
}

// setModelCacheVariables configures model cache volumes and environment variables
func (k *kubernetesOrchestrator) setModelCacheVariables(data *DeploymentManifestVariables, deployedCluster *v1.Cluster) error {
	modelCaches, err := util.GetClusterModelCache(*deployedCluster)
	if err != nil {
		return errors.Wrapf(err, "failed to get model cache for cluster %s", deployedCluster.Metadata.Name)
	}

	volumes, volumeMounts, modelEnv := generateModelCacheConfig(modelCaches)
	if len(volumes) > 0 {
		data.Volumes = append(data.Volumes, volumes...)
	}

	if len(volumeMounts) > 0 {
		data.VolumeMounts = append(data.VolumeMounts, volumeMounts...)
	}

	if modelEnv != nil {
		maps.Copy(data.Env, modelEnv)
	}

	return nil
}

// setModelArgs initializes model arguments from endpoint spec
func (k *kubernetesOrchestrator) setModelArgs(data *DeploymentManifestVariables, endpoint *v1.Endpoint, modelRegistry *v1.ModelRegistry) {
	modelArgs := map[string]interface{}{
		"name":          endpoint.Spec.Model.Name,
		"version":       endpoint.Spec.Model.Version,
		"file":          endpoint.Spec.Model.File,
		"task":          endpoint.Spec.Model.Task,
		"path":          endpoint.Spec.Model.Name, // default to model name
		"registry_type": string(modelRegistry.Spec.Type),
	}

	modelArgs["serve_name"] = endpoint.Spec.Model.Name

	// only set serve_name with version when version is specified and not latest for non-huggingface model registry
	if endpoint.Spec.Model.Version != "" && endpoint.Spec.Model.Version != v1.LatestVersion && modelRegistry.Spec.Type != v1.HuggingFaceModelRegistryType {
		modelArgs["serve_name"] = endpoint.Spec.Model.Name + ":" + endpoint.Spec.Model.Version
	}

	maps.Copy(data.ModelArgs, modelArgs)
}

// setModelRegistryVariables adapts model registry specific settings
func (k *kubernetesOrchestrator) setModelRegistryVariables(data *DeploymentManifestVariables, endpoint *v1.Endpoint,
	deployedCluster *v1.Cluster, modelRegistry *v1.ModelRegistry) error {
	modelCacheRelativePath := v1.DefaultModelCacheRelativePath

	modelCaches, err := util.GetClusterModelCache(*deployedCluster)
	if err != nil {
		return errors.Wrapf(err, "failed to get model caches")
	}

	// TODO: Now we only use the first model cache for simplicity, In the future, we may support specific model cache.
	if len(modelCaches) > 0 {
		modelCacheRelativePath = modelCaches[0].Name
	}

	switch modelRegistry.Spec.Type {
	case v1.BentoMLModelRegistryType:
		url, _ := url.Parse(modelRegistry.Spec.Url) // nolint: errcheck
		if url != nil && url.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			modelRealVersion, err := getDeployedModelRealVersion(modelRegistry, endpoint.Spec.Model.Name, endpoint.Spec.Model.Version)
			if err != nil {
				return errors.Wrapf(err, "failed to get deployed model real version for model %s", endpoint.Spec.Model.Name)
			}

			data.ModelArgs["version"] = modelRealVersion
			mountPath := filepath.Join("/mnt", "bentoml")
			// bentoml model registry path: <BENTOML_HOME>/models/<model_name>/<model_version>
			// so we need to append "models" to the path
			data.ModelArgs["registry_path"] = filepath.Join(mountPath, "models", endpoint.Spec.Model.Name, modelRealVersion)
			data.ModelArgs["path"] = filepath.Join(v1.DefaultK8sClusterModelCacheMountPath, modelCacheRelativePath, endpoint.Spec.Model.Name, modelRealVersion)

			data.Volumes = append(data.Volumes, corev1.Volume{
				Name: "bentoml-model-registry",
				VolumeSource: corev1.VolumeSource{
					NFS: &corev1.NFSVolumeSource{
						Server: url.Hostname(),
						Path:   url.Path,
					},
				},
			})

			data.VolumeMounts = append(data.VolumeMounts, corev1.VolumeMount{
				Name:      "bentoml-model-registry",
				MountPath: mountPath,
			})
		}

	case v1.HuggingFaceModelRegistryType:
		data.Env[v1.HFEndpoint] = strings.TrimSuffix(modelRegistry.Spec.Url, "/")
		if modelRegistry.Spec.Credentials != "" {
			data.Env[v1.HFTokenEnv] = modelRegistry.Spec.Credentials
		}

		modelRealVersion, err := getDeployedModelRealVersion(modelRegistry, endpoint.Spec.Model.Name, endpoint.Spec.Model.Version)
		if err != nil {
			return errors.Wrapf(err, "failed to get deployed model real version for model %s", endpoint.Spec.Model.Name)
		}

		data.ModelArgs["version"] = modelRealVersion
		data.ModelArgs["registry_path"] = endpoint.Spec.Model.Name
		data.ModelArgs["path"] = filepath.Join(v1.DefaultK8sClusterModelCacheMountPath, modelCacheRelativePath, endpoint.Spec.Model.Name, modelRealVersion)
	}

	return nil
}

// addSharedMemoryVolume adds shared memory volume to the deployment
func (k *kubernetesOrchestrator) addSharedMemoryVolume(data *DeploymentManifestVariables) {
	data.Volumes = append(data.Volumes, corev1.Volume{
		Name: "dshm",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				// none size limit mean the sharing memory size equals node allocated memory.
				Medium: corev1.StorageMediumMemory,
			},
		},
	})

	data.VolumeMounts = append(data.VolumeMounts, corev1.VolumeMount{
		Name:      "dshm",
		MountPath: "/dev/shm",
	})
}

func (k *kubernetesOrchestrator) buildManifestVariables(endpoint *v1.Endpoint, deployedCluster *v1.Cluster, modelRegistry *v1.ModelRegistry,
	engine *v1.Engine, imageRegistry *v1.ImageRegistry) (DeploymentManifestVariables, error) {
	// Initialize deployment manifest variables
	data := newDeploymentManifestVariables()

	// Set basic variables
	k.setBasicVariables(&data, endpoint, deployedCluster, engine)

	// Set deploy image variables
	if err := k.setDeployImageVariables(&data, endpoint, engine, imageRegistry); err != nil {
		return DeploymentManifestVariables{}, err
	}

	// Set routing logic
	k.setRoutingLogic(&data, endpoint)

	// Set engine args
	k.setEngineArgs(&data, endpoint, engine)

	// Set resource variables
	if err := k.setResourceVariables(&data, endpoint); err != nil {
		return DeploymentManifestVariables{}, err
	}

	// Set environment variables
	k.setEnvironmentVariables(&data, endpoint)

	// Set model cache variables
	if err := k.setModelCacheVariables(&data, deployedCluster); err != nil {
		return DeploymentManifestVariables{}, err
	}

	// Set model args
	k.setModelArgs(&data, endpoint, modelRegistry)

	// Set model registry specific variables
	if err := k.setModelRegistryVariables(&data, endpoint, deployedCluster, modelRegistry); err != nil {
		return DeploymentManifestVariables{}, err
	}

	// Add shared memory volume
	k.addSharedMemoryVolume(&data)

	return data, nil
}

func (k *kubernetesOrchestrator) getDeployTemplate(endpoint *v1.Endpoint, engine *v1.Engine) (string, error) {
	mode := "default"

	if endpoint.Spec.DeploymentOptions != nil && endpoint.Spec.DeploymentOptions["deploy_mode"] != nil {
		deployMode, ok := endpoint.Spec.DeploymentOptions["deploy_mode"].(string)
		if ok {
			mode = deployMode
		}
	}

	for _, version := range engine.Spec.Versions {
		if version.Version == endpoint.Spec.Engine.Version {
			// Use the new GetDeployTemplate method which handles Base64 decoding automatically
			template, err := version.GetDeployTemplate("kubernetes", mode)
			if err != nil {
				return "", errors.Wrapf(err, "failed to get deployment template for endpoint %s", endpoint.Metadata.WorkspaceName())
			}

			return template, nil
		}
	}

	return "", errors.Errorf("engine version %s not found for endpoint %s", endpoint.Spec.Engine.Version, endpoint.Metadata.WorkspaceName())
}

// getImageForAccelerator retrieves the appropriate container image for a specific accelerator type
// from the engine version specification. It returns the full image name and tag.
//
// Parameters:
//   - engine: The engine object containing version specifications
//   - version: The specific engine version to use
//   - acceleratorType: The accelerator type (e.g., "nvidia-gpu", "amd-gpu", "cpu")
//   - imagePrefix: The image registry prefix (e.g., "registry.neutree.ai/neutree")
//
// Returns:
//   - imageName: The full image name with registry prefix (e.g., "registry.neutree.ai/neutree/vllm")
//   - imageTag: The image tag (e.g., "v0.5.0")
//   - error: Any error encountered during image selection
func (k *kubernetesOrchestrator) getImageForAccelerator(engine *v1.Engine, version string, acceleratorType string) (string, string, error) {
	// Find the matching engine version
	var targetVersion *v1.EngineVersion

	for _, ev := range engine.Spec.Versions {
		if ev.Version == version {
			targetVersion = ev
			break
		}
	}

	if targetVersion == nil {
		return "", "", errors.Errorf("engine version %s not found in engine %s", version, engine.Metadata.Name)
	}

	// Check if the engine version has images configured
	if len(targetVersion.Images) == 0 {
		// Fallback to legacy behavior: use cluster image name convention
		imageName := engine.Metadata.Name
		return imageName, version, nil
	}

	// Default to "cpu" if no accelerator type is specified
	if acceleratorType == "" {
		acceleratorType = "cpu"
	}

	// Get image for the specific accelerator type
	engineImage := targetVersion.GetImageForAccelerator(acceleratorType)
	if engineImage == nil {
		// If accelerator type not found, try to use a default or return error
		supportedAccelerators := targetVersion.GetSupportedAccelerators()

		return "", "", errors.Errorf(
			"no image configured for accelerator type %s in engine %s version %s. Supported accelerators: %v",
			acceleratorType, engine.Metadata.Name, version, supportedAccelerators,
		)
	}

	// Construct full image name
	imageName := engineImage.ImageName

	imageTag := engineImage.Tag
	if imageTag == "" {
		imageTag = version // Fallback to engine version if tag is not specified
	}

	return imageName, imageTag, nil
}

func generateModelCacheConfig(modelCaches []v1.ModelCache) ([]corev1.Volume, []corev1.VolumeMount, map[string]string) {
	volumes := []corev1.Volume{}
	volumeMounts := []corev1.VolumeMount{}
	env := make(map[string]string)

	volumes = append(volumes, corev1.Volume{
		Name: "models-cache-tmp",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				Medium: corev1.StorageMediumDefault,
			},
		},
	})

	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      "models-cache-tmp",
		MountPath: v1.DefaultK8sClusterModelCacheMountPath,
	})

	for _, cache := range modelCaches {
		name := util.CacheName(cache)
		if cache.HostPath != nil {
			volumes = append(volumes, corev1.Volume{
				Name: name,
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: cache.HostPath.Path,
					},
				},
			})

			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      name,
				MountPath: path.Join(v1.DefaultK8sClusterModelCacheMountPath, cache.Name),
			})
		}

		if cache.NFS != nil {
			volumes = append(volumes, corev1.Volume{
				Name: name,
				VolumeSource: corev1.VolumeSource{
					NFS: &corev1.NFSVolumeSource{
						Server: cache.NFS.Server,
						Path:   cache.NFS.Path,
					},
				},
			})

			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      name,
				MountPath: path.Join(v1.DefaultK8sClusterModelCacheMountPath, cache.Name),
			})
		}

		if cache.PVC != nil {
			volumes = append(volumes, corev1.Volume{
				Name: name,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: name,
					},
				},
			})

			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      name,
				MountPath: path.Join(v1.DefaultK8sClusterModelCacheMountPath, cache.Name),
			})
		}
	}

	return volumes, volumeMounts, env
}

func newDeploymentManifestVariables() DeploymentManifestVariables {
	return DeploymentManifestVariables{
		Resources:    make(map[string]string),
		NodeSelector: make(map[string]string),
		Env:          make(map[string]string),
		ModelArgs:    make(map[string]interface{}),
		EngineArgs:   make(map[string]interface{}),
		Volumes:      []corev1.Volume{},
		VolumeMounts: []corev1.VolumeMount{},
	}
}
