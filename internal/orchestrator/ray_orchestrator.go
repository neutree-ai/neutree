package orchestrator

import (
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/model_registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// isNewClusterVersion returns true if the cluster version is > v1.0.0.
// Clusters with empty version or <= v1.0.0 are considered old.
func isNewClusterVersion(cluster *v1.Cluster) bool {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.Version == "" {
		return false
	}

	isNew, err := semver.LessThan("v1.0.0", cluster.Spec.Version)

	return err == nil && isNew
}

var _ Orchestrator = &RayOrchestrator{}

type RayOrchestrator struct {
	cluster *v1.Cluster

	storage        storage.Storage
	acceleratorMgr accelerator.Manager
}

type RayOptions struct {
	Options
}

func NewRayOrchestrator(opts RayOptions) *RayOrchestrator {
	o := &RayOrchestrator{
		cluster:        opts.Cluster,
		storage:        opts.Storage,
		acceleratorMgr: opts.AcceleratorMgr,
	}

	return o
}

func (o *RayOrchestrator) getDashboardService() (dashboard.DashboardService, error) {
	if o.cluster.Status == nil || o.cluster.Status.DashboardURL == "" {
		return nil, errors.New("dashboard URL is not configured in cluster status")
	}

	return dashboard.NewDashboardService(o.cluster.Status.DashboardURL), nil
}

func (o *RayOrchestrator) prepareOrchestratorContext(endpoint *v1.Endpoint) (*OrchestratorContext, error) {
	deployedCluster, err := getEndpointDeployCluster(o.storage, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get deploy cluster")
	}

	engine, err := getUsedEngine(o.storage, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get engine")
	}

	modelRegistry, err := getEndpointModelRegistry(o.storage, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get model registry")
	}

	imageRegistry, err := getUsedImageRegistries(deployedCluster, o.storage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get used image registry")
	}

	dashboardService, err := o.getDashboardService()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get dashboard service")
	}

	return &OrchestratorContext{
		Cluster:       deployedCluster,
		Engine:        engine,
		ModelRegistry: modelRegistry,
		ImageRegistry: imageRegistry,
		Endpoint:      endpoint,
		rayService:    dashboardService,
		logger:        klog.LoggerWithValues(klog.Background(), "endpoint", endpoint.Metadata.WorkspaceName()),
	}, nil
}

func (o *RayOrchestrator) validateDependencies(ctx *OrchestratorContext) error {
	// validate cluster status
	if ctx.Cluster.Status == nil || ctx.Cluster.Status.Phase != v1.ClusterPhaseRunning {
		return errors.Errorf("deploy cluster %s is not running", ctx.Cluster.Metadata.WorkspaceName())
	}

	if ctx.Cluster.Spec.Type != v1.SSHClusterType {
		return errors.Errorf("deploy cluster %s is not ssh type", ctx.Cluster.Metadata.WorkspaceName())
	}

	// validate engine status
	if ctx.Engine.Status == nil || ctx.Engine.Status.Phase != v1.EnginePhaseCreated {
		return errors.Errorf("engine %s not ready", ctx.Engine.Metadata.WorkspaceName())
	}

	// validate model registry status
	if ctx.ModelRegistry.Status == nil || ctx.ModelRegistry.Status.Phase != v1.ModelRegistryPhaseCONNECTED {
		return errors.Errorf("model registry %s not ready", ctx.ModelRegistry.Metadata.WorkspaceName())
	}

	// validate image registry status
	if ctx.ImageRegistry.Status == nil || ctx.ImageRegistry.Status.Phase != v1.ImageRegistryPhaseCONNECTED {
		return errors.Errorf("image registry %s not ready", ctx.ImageRegistry.Metadata.WorkspaceName())
	}

	return nil
}

// CreateEndpoint deploys a new endpoint using Ray Serve.
func (o *RayOrchestrator) CreateEndpoint(endpoint *v1.Endpoint) error {
	ctx, err := o.prepareOrchestratorContext(endpoint)
	if err != nil {
		return errors.Wrapf(err, "failed to prepare context for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	err = o.validateDependencies(ctx)
	if err != nil {
		return errors.Wrapf(err, "failed to validate dependencies for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	ctx.logger.V(4).Info("Creating or updating endpoint in Ray Serve")
	// For clusters <= v1.0.0, NFS is mounted inside ray_container via SSH.
	// For clusters > v1.0.0, NFS is mounted via engine container run_options, so skip connect.
	if !isNewClusterVersion(o.cluster) {
		// always exec connect model to cluster, for cluster may dynamic scale, we need ensure model exists on all cluster nodes.
		// todo: In order to reduce model connection actions, a new controller may be created in the future to uniformly manage model connections on the cluster.
		err = o.connectSSHClusterEndpointModel(*ctx.ModelRegistry, *ctx.Endpoint, connect)
		if err != nil {
			return errors.Wrapf(err, "failed to connect model registry before creating endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
		}
	}

	return o.createOrUpdate(ctx)
}

func (o *RayOrchestrator) PauseEndpoint(endpoint *v1.Endpoint) error {
	ctx, err := o.prepareOrchestratorContext(endpoint)
	if err != nil {
		return errors.Wrapf(err, "failed to prepare context for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	ctx.logger.V(4).Info("Pausing endpoint by deleting from Ray Serve")

	err = o.deleteEndpoint(ctx)
	if err != nil {
		return errors.Wrapf(err, "failed to pause endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	return nil
}

func (o *RayOrchestrator) createOrUpdate(ctx *OrchestratorContext) error {
	currentAppsResp, err := ctx.rayService.GetServeApplications()
	if err != nil {
		return errors.Wrapf(err, "failed to get current serve applications for endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
	}

	newApp, err := EndpointToApplication(ctx.Endpoint, ctx.Cluster, ctx.ModelRegistry, ctx.Engine, ctx.ImageRegistry, o.acceleratorMgr)
	if err != nil {
		return errors.Wrapf(err, "failed to convert endpoint to application for endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
	}

	// Build the list of applications for the PUT request
	needAppend := true
	needUpdate := true

	updatedAppsList := make([]dashboard.RayServeApplication, 0, len(currentAppsResp.Applications)+1)

	for _, appStatus := range currentAppsResp.Applications {
		if appStatus.DeployedAppConfig != nil {
			updatedAppsList = append(updatedAppsList, *appStatus.DeployedAppConfig)

			if appStatus.DeployedAppConfig.Name == newApp.Name {
				needAppend = false

				equal, diff, err := util.JsonEqual(appStatus.DeployedAppConfig, newApp)
				if err != nil {
					return errors.Wrapf(err, "failed to compare serve application for endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
				}

				if equal {
					needUpdate = false
				} else {
					ctx.logger.Info("Serve application need to update", "diff", diff)

					updatedAppsList[len(updatedAppsList)-1] = newApp
				}
			}
		}
	}

	if needAppend {
		updatedAppsList = append(updatedAppsList, newApp)
	}

	if needAppend || needUpdate {
		updateReq := dashboard.RayServeApplicationsRequest{
			Applications: updatedAppsList,
		}

		err = ctx.rayService.UpdateServeApplications(updateReq)
		if err != nil {
			return errors.Wrapf(err, "failed to update serve applications for endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
		}
	}

	return nil
}

// DeleteEndpoint removes an endpoint from Ray Serve.
func (o *RayOrchestrator) DeleteEndpoint(endpoint *v1.Endpoint) error {
	// delete endpoint should not validate dependencies
	ctx, err := o.prepareOrchestratorContext(endpoint)
	if err != nil {
		return errors.Wrapf(err, "failed to prepare context for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	// For clusters <= v1.0.0, NFS is mounted inside ray_container via SSH — need to disconnect.
	// For clusters > v1.0.0, NFS is mounted via engine container run_options, so skip disconnect.
	if !isNewClusterVersion(o.cluster) {
		err = o.connectSSHClusterEndpointModel(*ctx.ModelRegistry, *ctx.Endpoint, disconnect)
		if err != nil {
			return errors.Wrapf(err, "failed to disconnect model registry before deleting endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
		}
	}

	ctx.logger.V(4).Info("Deleting endpoint from Ray Serve")

	return o.deleteEndpoint(ctx)
}

func (o *RayOrchestrator) deleteEndpoint(ctx *OrchestratorContext) error {
	currentAppsResp, err := ctx.rayService.GetServeApplications()
	if err != nil {
		return errors.Wrapf(err, "failed to get current serve applications before deletion of endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
	}

	// Build the list of applications excluding the one to delete
	updatedAppsList := make([]dashboard.RayServeApplication, 0, len(currentAppsResp.Applications))
	found := false

	for name, appStatus := range currentAppsResp.Applications {
		if name == EndpointToServeApplicationName(ctx.Endpoint) {
			found = true
			continue // Skip the endpoint to be deleted
		}

		// When the application is deleted, the deployed application configuration is empty, ignored it.
		if appStatus.DeployedAppConfig != nil {
			updatedAppsList = append(updatedAppsList, *appStatus.DeployedAppConfig)
		}
	}

	if !found {
		// Endpoint not found, consider it successfully deleted (idempotency)
		ctx.logger.Info("Endpoint not found during deletion, assuming already deleted")
		return nil
	}

	updateReq := dashboard.RayServeApplicationsRequest{
		Applications: updatedAppsList,
	}

	err = ctx.rayService.UpdateServeApplications(updateReq)
	if err != nil {
		return errors.Wrapf(err, "failed to update serve applications for deletion of endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
	}

	return nil
}

// GetEndpointStatus retrieves the status of a specific endpoint from Ray Serve.
func (o *RayOrchestrator) GetEndpointStatus(endpoint *v1.Endpoint) (*v1.EndpointStatus, error) {
	// Placeholder implementation: Get all apps and check if ours exists.
	// A more robust implementation would query the specific app status if the API supports it,
	// or parse the status field from the GetServeApplications response.
	dashboardService, err := o.getDashboardService()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get dashboard service for getting endpoint %s status", endpoint.Metadata.WorkspaceName())
	}

	currentAppsResp, err := dashboardService.GetServeApplications()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get current serve applications for endpoint %s status", endpoint.Metadata.WorkspaceName())
	}

	isDeleting := endpoint.GetDeletionTimestamp() != ""
	status, exists := currentAppsResp.Applications[EndpointToServeApplicationName(endpoint)]

	if isDeleting {
		if !exists {
			return &v1.EndpointStatus{
				Phase:        v1.EndpointPhaseDELETED,
				ErrorMessage: "",
			}, nil
		}

		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseDELETING,
			ErrorMessage: "Endpoint deleting in progress: waiting for Ray Serve to delete the application",
		}, nil
	}

	isPaused := IsEndpointPaused(endpoint)
	if isPaused {
		if !exists {
			return &v1.EndpointStatus{
				Phase:        v1.EndpointPhasePAUSED,
				ErrorMessage: "",
			}, nil
		}

		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseDEPLOYING,
			ErrorMessage: "Endpoint pausing in progress: waiting for Ray Serve to delete the application",
		}, nil
	}

	if !exists {
		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseDEPLOYING,
			ErrorMessage: "Endpoint deploying in progress: Endpoint not found in Ray Serve applications",
		}, nil
	}

	proxyReady := true

	if len(currentAppsResp.Proxies) == 0 {
		proxyReady = false
	}

	for _, proxyStatus := range currentAppsResp.Proxies {
		if proxyStatus.Status != dashboard.ProxyStatusHealthy {
			proxyReady = false
			break
		}
	}

	// Basic status mapping
	// https://docs.ray.io/en/latest/serve/api/doc/ray.serve.schema.ApplicationStatus.html#ray.serve.schema.ApplicationStatus
	var phase v1.EndpointPhase
	var errorMessages []string

	switch status.Status {
	case "DEPLOYING", "NOT_STARTED":
		phase = v1.EndpointPhaseDEPLOYING
	case "DEPLOY_FAILED", "UNHEALTHY":
		phase = v1.EndpointPhaseFAILED
	case "RUNNING":
		if !proxyReady {
			phase = v1.EndpointPhaseDEPLOYING

			errorMessages = append(errorMessages, "Proxy not healthy")
		} else {
			phase = v1.EndpointPhaseRUNNING
		}
	default:
		phase = v1.EndpointPhaseDEPLOYING

		errorMessages = append(errorMessages, fmt.Sprintf("Unknown application status: %s", status.Status))
	}

	if phase == v1.EndpointPhaseRUNNING {
		return &v1.EndpointStatus{
			Phase:        phase,
			ErrorMessage: "",
		}, nil
	}

	// Merge Ray Serve error messages
	if status.Message != "" {
		errorMessages = append(errorMessages, status.Message)
	}

	if len(status.Deployments) == 0 {
		errorMessages = append(errorMessages, "No deployments found for the application")
	}

	// merge Ray Serve error messages
	for _, deployment := range status.Deployments {
		if deployment.Status != dashboard.DeploymentStatusHealthy && deployment.Message != "" {
			errorMessages = append(errorMessages, fmt.Sprintf("Deployment %s: %s", deployment.Name, deployment.Message))
		}
	}

	errorMsg := strings.Join(errorMessages, "; ")
	// Add prefix to error message based on phase
	if errorMsg != "" {
		switch phase {
		// no prefix
		case v1.EndpointPhaseDEPLOYING:
			errorMsg = "Endpoint deploying in progress: " + errorMsg
		case v1.EndpointPhaseFAILED:
			errorMsg = "Endpoint failed: " + errorMsg
		}
	}

	endpointStatus := &v1.EndpointStatus{
		Phase:        phase,
		ErrorMessage: errorMsg, // Use merged error message
	}

	return endpointStatus, nil
}

// endpointToApplication converts Neutree Endpoint and ModelRegistry to a RayServeApplication.
func EndpointToApplication(endpoint *v1.Endpoint, deployedCluster *v1.Cluster,
	modelRegistry *v1.ModelRegistry, engine *v1.Engine, imageRegistry *v1.ImageRegistry,
	acceleratorMgr accelerator.Manager) (dashboard.RayServeApplication, error) {
	app := dashboard.RayServeApplication{
		Name:        EndpointToServeApplicationName(endpoint),
		RoutePrefix: fmt.Sprintf("/%s/%s", endpoint.Metadata.Workspace, endpoint.Metadata.Name),
		ImportPath: fmt.Sprintf("serve.%s.%s.app:app_builder", strings.ReplaceAll(endpoint.Spec.Engine.Engine, "-", "_"),
			strings.ReplaceAll(endpoint.Spec.Engine.Version, ".", "_")),
		Args: map[string]interface{}{},
	}

	// Make a shallow copy of deployment options so we can safely adjust scheduler type for Ray
	deploymentOptions := maps.Clone(endpoint.Spec.DeploymentOptions)
	if deploymentOptions == nil {
		deploymentOptions = make(map[string]interface{})
	}

	// Normalize scheduler type: API layer accepts "roundrobin" as alias; Ray expects "pow2"
	if schedulerRaw, ok := deploymentOptions["scheduler"].(map[string]interface{}); ok {
		if schedulerType, ok := schedulerRaw["type"].(string); ok && strings.EqualFold(schedulerType, "roundrobin") {
			schedulerRaw["type"] = "pow2"
			deploymentOptions["scheduler"] = schedulerRaw
		}
	}

	rayResource, err := convertToRay(acceleratorMgr, endpoint.Spec.Resources)
	if err != nil {
		klog.Errorf("Failed to convert endpoint %s resources to Ray format: %v", endpoint.Metadata.WorkspaceName(), err)
		return dashboard.RayServeApplication{}, err
	}

	backendConfig := map[string]interface{}{
		"num_replicas": endpoint.Spec.Replicas.Num,
		"num_cpus":     rayResource.NumCPUs,
		"memory":       rayResource.Memory,
		"num_gpus":     rayResource.NumGPUs,
		"resources":    rayResource.Resources,
	}

	deploymentOptions["backend"] = backendConfig

	deploymentOptions["controller"] = map[string]interface{}{
		"num_replicas": 1,
		"num_cpus":     0.1,
		"num_gpus":     0,
	}

	app.Args["deployment_options"] = deploymentOptions

	applicationEnv := map[string]string{}

	for k, v := range endpoint.Spec.Env {
		applicationEnv[k] = v
	}

	modelArgs := map[string]interface{}{
		"registry_type": modelRegistry.Spec.Type,
		"name":          endpoint.Spec.Model.Name,
		"file":          endpoint.Spec.Model.File,
		"version":       endpoint.Spec.Model.Version,
		"task":          endpoint.Spec.Model.Task,
	}

	modelArgs["serve_name"] = endpoint.Spec.Model.Name
	if endpoint.Spec.Model.Version != "" && endpoint.Spec.Model.Version != v1.LatestVersion && modelRegistry.Spec.Type != v1.HuggingFaceModelRegistryType {
		modelArgs["serve_name"] = endpoint.Spec.Model.Name + ":" + endpoint.Spec.Model.Version
	}

	modelCacheRelativePath := v1.DefaultModelCacheRelativePath

	modelCaches, err := util.GetClusterModelCache(*deployedCluster)
	if err != nil {
		return dashboard.RayServeApplication{}, errors.Wrap(err, "failed to get cluster model cache")
	}

	// TODO: Now we only use the first model cache for simplicity, In the future, we may support specific model cache.
	if len(modelCaches) > 0 {
		modelCacheRelativePath = modelCaches[0].Name
	}

	switch modelRegistry.Spec.Type {
	case v1.BentoMLModelRegistryType:
		registryURL, _ := url.Parse(modelRegistry.Spec.Url) // nolint: errcheck
		if registryURL != nil && registryURL.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			modelRealVersion, err := getDeployedModelRealVersion(modelRegistry, endpoint.Spec.Model.Name, endpoint.Spec.Model.Version)
			if err != nil {
				return dashboard.RayServeApplication{}, errors.Wrapf(err, "failed to get real model version for model %s", endpoint.Spec.Model.Name)
			}

			nfsMountPath := filepath.Join("/mnt", endpoint.Metadata.Workspace, endpoint.Metadata.Name)

			modelArgs["version"] = modelRealVersion
			// bentoml model registry path: <BENTOML_HOME>/models/<model_name>/<model_version>
			// so we need to append "models" to the path
			modelArgs["registry_path"] = filepath.Join(nfsMountPath, "models", endpoint.Spec.Model.Name, modelRealVersion)
			modelArgs["path"] = filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, modelCacheRelativePath, endpoint.Spec.Model.Name, modelRealVersion)
		}
	case v1.HuggingFaceModelRegistryType:
		applicationEnv[v1.HFEndpoint] = strings.TrimSuffix(modelRegistry.Spec.Url, "/")
		if modelRegistry.Spec.Credentials != "" {
			applicationEnv[v1.HFTokenEnv] = modelRegistry.Spec.Credentials
		}

		modelRealVersion, err := getDeployedModelRealVersion(modelRegistry, endpoint.Spec.Model.Name, endpoint.Spec.Model.Version)
		if err != nil {
			return dashboard.RayServeApplication{}, errors.Wrapf(err, "failed to get deployed model real version for model %s", endpoint.Spec.Model.Name)
		}

		modelArgs["version"] = modelRealVersion
		modelArgs["registry_path"] = endpoint.Spec.Model.Name
		modelArgs["path"] = filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, modelCacheRelativePath, endpoint.Spec.Model.Name, modelRealVersion)
	}

	app.Args["model"] = modelArgs

	maps.Copy(app.Args, endpoint.Spec.Variables)

	setEngineSpecialEnv(endpoint, deployedCluster, applicationEnv)

	app.RuntimeEnv = map[string]interface{}{
		"env_vars": applicationEnv,
	}

	// Generate runtime_env.container for engine version isolation (SSH clusters > v1.0.0).
	// The engine image runs as a sibling container on the host via docker.sock.
	// Clusters <= v1.0.0 run the engine inside ray_container directly.
	if isNewClusterVersion(deployedCluster) {
		containerConfig, err := buildEngineContainerConfig(endpoint, deployedCluster, engine, imageRegistry, acceleratorMgr, modelCaches, modelRegistry)
		if err != nil {
			return dashboard.RayServeApplication{}, errors.Wrapf(err, "failed to build engine container config for endpoint %s", endpoint.Metadata.WorkspaceName())
		}

		if containerConfig != nil {
			app.RuntimeEnv["container"] = containerConfig
		}
	}

	return app, nil
}

// buildEngineContainerConfig constructs the runtime_env.container config for
// running the engine in an isolated container via runtime_env.container.
// The caller must ensure this is only called for SSH clusters with version > v1.0.0.
// Returns an error if the SSH engine image is not found or accelerator runtime config fails.
// For BentoML NFS model registries, a Docker NFS volume mount option is appended to run_options
// so the engine container can access the model registry directly.
func buildEngineContainerConfig(endpoint *v1.Endpoint, cluster *v1.Cluster,
	engine *v1.Engine, imageRegistry *v1.ImageRegistry,
	acceleratorMgr accelerator.Manager,
	modelCaches []v1.ModelCache, modelRegistry *v1.ModelRegistry) (map[string]interface{}, error) {
	if engine == nil || engine.Spec == nil {
		return nil, errors.New("engine is required for SSH cluster")
	}

	// Find the matching engine version
	var targetVersion *v1.EngineVersion
	for _, ev := range engine.Spec.Versions {
		if ev.Version == endpoint.Spec.Engine.Version {
			targetVersion = ev
			break
		}
	}

	if targetVersion == nil {
		return nil, errors.Errorf("engine version %s not found in engine %s", endpoint.Spec.Engine.Version, engine.Metadata.Name)
	}

	// Get cluster accelerator type
	acceleratorType := ""
	if cluster.Status != nil && cluster.Status.AcceleratorType != nil {
		acceleratorType = *cluster.Status.AcceleratorType
	}

	// Look up engine image using accelerator type key (shared with K8s)
	engineImage := targetVersion.GetImageForAccelerator(acceleratorType)

	if engineImage == nil {
		return nil, errors.Errorf("no engine image configured for accelerator %q in engine %s version %s",
			acceleratorType, engine.Metadata.Name, endpoint.Spec.Engine.Version)
	}

	// Build image reference with registry prefix
	imageRef := engineImage.ImageName + ":" + engineImage.Tag
	if imageRegistry != nil {
		imagePrefix, err := util.GetImagePrefix(imageRegistry)
		if err == nil && imagePrefix != "" {
			imageRef = imagePrefix + "/" + engineImage.ImageName + ":" + engineImage.Tag
		}
	}

	// Get accelerator-specific run_options from plugin
	var runOptions []string
	if acceleratorMgr != nil && acceleratorType != "" {
		opts, err := acceleratorMgr.GetEngineContainerRunOptions(acceleratorType)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get engine container run options for accelerator %s", acceleratorType)
		}

		runOptions = append(runOptions, opts...)
	}

	// Mount model caches using HOST paths (docker.sock creates containers on host)
	for _, mc := range modelCaches {
		if mc.HostPath != nil {
			containerMountPath := filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, mc.Name)
			runOptions = append(runOptions, fmt.Sprintf("-v %s:%s", mc.HostPath.Path, containerMountPath))
		}
	}

	// Mount NFS model registry directly into the engine container via Docker NFS volume.
	if modelRegistry != nil && modelRegistry.Spec != nil && modelRegistry.Spec.Type == v1.BentoMLModelRegistryType {
		registryURL, err := url.Parse(modelRegistry.Spec.Url)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse model registry URL %s", modelRegistry.Spec.Url)
		}

		if registryURL.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			registry, err := model_registry.NewModelRegistry(modelRegistry)
			if err != nil {
				return nil, errors.Wrap(err, "failed to create model registry for NFS type detection")
			}

			nfsType, err := registry.GetNFSType()
			if err != nil {
				return nil, errors.Wrap(err, "failed to detect NFS type from control-plane mount")
			}

			if nfsType == "" {
				return nil, errors.New("NFS mount not found on control-plane, cannot determine NFS type for engine container")
			}

			nfsMountPath := filepath.Join("/mnt", endpoint.Metadata.Workspace, endpoint.Metadata.Name)
			runOptions = append(runOptions, fmt.Sprintf(
				"--mount type=volume,dst=%s,volume-opt=type=%s,volume-opt=o=addr=%s,volume-opt=device=:%s",
				nfsMountPath, nfsType, registryURL.Hostname(), registryURL.Path))
		}
	}

	// Inject HAMi-core for fractional GPU memory isolation.
	// When gpu < 1.0, calculate absolute memory limit from per-device memory and inject
	// libvgpu.so via LD_PRELOAD to enforce the limit.
	if endpoint.Spec.Resources != nil && cluster.Status != nil && cluster.Status.ResourceInfo != nil {
		gpuCount := endpoint.Spec.Resources.GetGPUCount()
		if gpuCount > 0 && gpuCount < 1 {
			acceleratorType := v1.AcceleratorType("")
			if cluster.Status.AcceleratorType != nil {
				acceleratorType = v1.AcceleratorType(*cluster.Status.AcceleratorType)
			}

			if group, ok := cluster.Status.ResourceInfo.Allocatable.AcceleratorGroups[acceleratorType]; ok && group.MemoryPerDeviceMiB > 0 {
				memLimitMiB := int64(float64(group.MemoryPerDeviceMiB) * gpuCount)
				runOptions = append(runOptions,
					fmt.Sprintf("-v %s:%s:ro", v1.HamiCoreHostLibPath, v1.HamiCoreLibPath),
					fmt.Sprintf("-e LD_PRELOAD=%s", v1.HamiCoreLibPath),
					fmt.Sprintf("-e CUDA_DEVICE_MEMORY_LIMIT=%dm", memLimitMiB),
				)
			}
		}
	}

	// Auto-remove engine container when it exits to prevent residual containers on the host.
	// Engine containers run in foreground (no -d), so Ray collects exit codes and logs via
	// the docker run process pipe before --rm triggers cleanup.
	runOptions = append(runOptions, "--rm")

	return map[string]interface{}{
		"image":       imageRef,
		"run_options": runOptions,
	}, nil
}

func setEngineSpecialEnv(endpoint *v1.Endpoint, deployedCluster *v1.Cluster, applicationEnv map[string]string) {
	// Old clusters (<= v1.0.0) use RAY_kill_child_processes_on_worker_exit_with_raylet_subreaper which causes
	// parent processes to lose child exit codes, breaking vLLM's P2P check. For those clusters, skip the check.
	// New clusters (> v1.0.0) use RAY_process_group_cleanup_enabled which doesn't have this issue.
	if endpoint.Spec != nil && endpoint.Spec.Engine != nil && endpoint.Spec.Engine.Engine == "vllm" {
		if deployedCluster.Spec != nil && deployedCluster.Spec.Version != "" {
			isNew, err := semver.LessThan("v1.0.0", deployedCluster.Spec.Version)
			if err == nil && !isNew {
				applicationEnv["VLLM_SKIP_P2P_CHECK"] = "1"
			}
		}
	}
}

// formatServiceURL constructs the service URL for an endpoint.
func FormatServiceURL(cluster *v1.Cluster, endpoint *v1.Endpoint) (string, error) {
	if cluster.Status == nil || cluster.Status.DashboardURL == "" {
		return "", errors.New("cluster dashboard URL is not available")
	}

	dashboardURL, err := url.Parse(cluster.Status.DashboardURL)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse cluster dashboard URL")
	}
	// Ray serve applications are typically exposed on port 8000 by default
	return fmt.Sprintf("%s://%s:8000/%s/%s", dashboardURL.Scheme, dashboardURL.Hostname(), endpoint.Metadata.Workspace, endpoint.Metadata.Name), nil
}

func EndpointToServeApplicationName(endpoint *v1.Endpoint) string {
	return fmt.Sprintf("%s_%s", endpoint.Metadata.Workspace, endpoint.Metadata.Name)
}
