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
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

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

// CreateEndpoint deploys a new endpoint using Ray Serve.
func (o *RayOrchestrator) CreateEndpoint(endpoint *v1.Endpoint) (*v1.EndpointStatus, error) {
	if endpoint.Spec.Replicas.Num != nil && *endpoint.Spec.Replicas.Num == 0 {
		// If replicas is set to 0, we treat it as a deletion request.
		klog.Infof("Endpoint %s replicas set to 0, treating as deletion request.", endpoint.Metadata.WorkspaceName())

		err := o.deleteEndpoint(endpoint)
		if err != nil {
			return nil, errors.Wrap(err, "failed to set endpoint with 0 replicas")
		}

		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseRUNNING,
			ErrorMessage: "",
		}, nil
	}

	return o.createOrUpdate(endpoint)
}

func (o *RayOrchestrator) createOrUpdate(endpoint *v1.Endpoint) (*v1.EndpointStatus, error) {
	// pre-check related resources
	deployedCluster, err := getEndpointDeployCluster(o.storage, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list cluster")
	}

	_, err = getUsedEngine(o.storage, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list engine")
	}

	modelRegistry, err := getEndpointModelRegistry(o.storage, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list model registry")
	}

	// call ray dashboard API
	dashboardService, err := o.getDashboardService()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get dashboard service for creating endpoint")
	}

	currentAppsResp, err := dashboardService.GetServeApplications()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get current serve applications")
	}

	newApp, err := EndpointToApplication(endpoint, deployedCluster, modelRegistry, o.acceleratorMgr)
	if err != nil {
		return nil, errors.Wrap(err, "failed to convert endpoint to application")
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
					return &v1.EndpointStatus{
						Phase:        v1.EndpointPhaseFAILED,
						ErrorMessage: errors.Wrap(err, "failed to compare serve application").Error(),
					}, nil // Return nil error as the operation failed but we captured status
				}

				if equal {
					needUpdate = false
				} else {
					klog.Infof("Serve application diff: %s, need to update", diff)

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

		err = dashboardService.UpdateServeApplications(updateReq)
		if err != nil {
			return &v1.EndpointStatus{
				Phase:        v1.EndpointPhaseFAILED,
				ErrorMessage: errors.Wrap(err, "failed to update serve applications").Error(),
			}, nil // Return nil error as the operation failed but we captured status
		}
	}

	return &v1.EndpointStatus{
		Phase:        v1.EndpointPhaseRUNNING,
		ErrorMessage: "",
	}, nil
}

// DeleteEndpoint removes an endpoint from Ray Serve.
func (o *RayOrchestrator) DeleteEndpoint(endpoint *v1.Endpoint) error {
	return o.deleteEndpoint(endpoint)
}

func (o *RayOrchestrator) deleteEndpoint(endpoint *v1.Endpoint) error {
	// pre-check cluster
	_, err := getEndpointDeployCluster(o.storage, endpoint)
	if err != nil {
		return errors.Wrap(err, "failed to list cluster")
	}

	dashboardService, err := o.getDashboardService()
	if err != nil {
		return errors.Wrap(err, "failed to get dashboard service for deleting endpoint")
	}

	currentAppsResp, err := dashboardService.GetServeApplications()
	if err != nil {
		return errors.Wrap(err, "failed to get current serve applications before deletion")
	}

	// Build the list of applications excluding the one to delete
	updatedAppsList := make([]dashboard.RayServeApplication, 0, len(currentAppsResp.Applications))
	found := false

	for name, appStatus := range currentAppsResp.Applications {
		if name == EndpointToServeApplicationName(endpoint) {
			found = true
			continue // Skip the endpoint to be deleted
		}

		updatedAppsList = append(updatedAppsList, *appStatus.DeployedAppConfig)
	}

	if !found {
		// Endpoint not found, consider it successfully deleted (idempotency)
		klog.Infof("Endpoint %s not found during deletion, assuming already deleted.\n", endpoint.Metadata.Name)
		return nil
	}

	updateReq := dashboard.RayServeApplicationsRequest{
		Applications: updatedAppsList,
	}

	err = dashboardService.UpdateServeApplications(updateReq)
	if err != nil {
		return errors.Wrap(err, "failed to update serve applications for deletion")
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
		return nil, errors.Wrap(err, "failed to get dashboard service for getting endpoint status")
	}

	currentAppsResp, err := dashboardService.GetServeApplications()
	if err != nil {
		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseFAILED,
			ErrorMessage: errors.Wrap(err, "failed to get serve applications to check status").Error(),
		}, nil
	}

	// For zero replicas, the Running phase indicates successful deletion of the application.
	// Therefore, here we check if the application still exists.
	// This differs from the behavior when the number of replicas is greater than 0.
	expectZeroReplicas := endpoint.Spec.Replicas.Num != nil && *endpoint.Spec.Replicas.Num == 0
	status, exists := currentAppsResp.Applications[EndpointToServeApplicationName(endpoint)]

	switch {
	case !exists && expectZeroReplicas:
		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseRUNNING,
			ErrorMessage: "",
		}, nil
	case exists && expectZeroReplicas:
		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhasePENDING,
			ErrorMessage: "Endpoint deletion in progress: still exists in Ray Serve applications",
		}, nil
	case !exists && !expectZeroReplicas:
		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhasePENDING,
			ErrorMessage: "Endpoint not found in Ray Serve applications",
		}, nil
	default:
		// Basic status mapping
		// https://docs.ray.io/en/latest/serve/api/doc/ray.serve.schema.ApplicationStatus.html#ray.serve.schema.ApplicationStatus
		var phase v1.EndpointPhase

		switch status.Status {
		case "RUNNING", "DELETING", "DEPLOYING", "UNHEALTHY", "NOT_STARTED":
			phase = v1.EndpointPhaseRUNNING
		case "DEPLOY_FAILED":
			phase = v1.EndpointPhaseFAILED
		default:
			phase = v1.EndpointPhaseRUNNING
		}

		return &v1.EndpointStatus{
			Phase:        phase,
			ErrorMessage: status.Message, // Use message from Ray if available
		}, nil
	}
}

func (o *RayOrchestrator) ConnectEndpointModel(endpoint *v1.Endpoint) error {
	modelRegistry, err := getEndpointModelRegistry(o.storage, endpoint)
	if err != nil {
		return errors.Wrap(err, "failed to get model registry")
	}

	op := connect

	switch o.cluster.Spec.Type {
	case v1.SSHClusterType:
		return o.connectSSHClusterEndpointModel(*modelRegistry, *endpoint, op)
	case v1.KubernetesClusterType:
		return o.connectKubernetesClusterEndpointModel(*modelRegistry, *endpoint, op)
	default:
		return fmt.Errorf("unsupported cluster type: %s", o.cluster.Spec.Type)
	}
}

func (o *RayOrchestrator) DisconnectEndpointModel(endpoint *v1.Endpoint) error {
	modelRegistry, err := getEndpointModelRegistry(o.storage, endpoint)
	if err != nil {
		return errors.Wrap(err, "failed to get model registry")
	}

	op := disconnect

	switch o.cluster.Spec.Type {
	case v1.SSHClusterType:
		return o.connectSSHClusterEndpointModel(*modelRegistry, *endpoint, op)
	case v1.KubernetesClusterType:
		return o.connectKubernetesClusterEndpointModel(*modelRegistry, *endpoint, op)
	default:
		return fmt.Errorf("unsupported cluster type: %s", o.cluster.Spec.Type)
	}
}

// endpointToApplication converts Neutree Endpoint and ModelRegistry to a RayServeApplication.
func EndpointToApplication(endpoint *v1.Endpoint, deployedCluster *v1.Cluster,
	modelRegistry *v1.ModelRegistry, acceleratorMgr accelerator.Manager) (dashboard.RayServeApplication, error) {
	app := dashboard.RayServeApplication{
		Name:        EndpointToServeApplicationName(endpoint),
		RoutePrefix: fmt.Sprintf("/%s/%s", endpoint.Metadata.Workspace, endpoint.Metadata.Name),
		ImportPath: fmt.Sprintf("serve.%s.%s.app:app_builder", strings.ReplaceAll(endpoint.Spec.Engine.Engine, "-", "_"),
			strings.ReplaceAll(endpoint.Spec.Engine.Version, ".", "_")),
		Args: map[string]interface{}{},
	}

	rayResource, err := convertToRay(acceleratorMgr, endpoint.Spec.Resources)
	if err != nil {
		klog.Errorf("Failed to convert resources to Ray format: %v", err)
		return dashboard.RayServeApplication{}, err
	}

	backendConfig := map[string]interface{}{
		"num_replicas": endpoint.Spec.Replicas.Num,
		"num_cpus":     rayResource.NumCPUs,
		"memory":       rayResource.Memory,
		"num_gpus":     rayResource.NumGPUs,
		"resources":    rayResource.Resources,
	}

	endpoint.Spec.DeploymentOptions["backend"] = backendConfig

	endpoint.Spec.DeploymentOptions["controller"] = map[string]interface{}{
		"num_replicas": 1,
		"num_cpus":     0.1,
		"num_gpus":     0,
	}

	app.Args["deployment_options"] = endpoint.Spec.DeploymentOptions

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
		url, _ := url.Parse(modelRegistry.Spec.Url) // nolint: errcheck
		// todo: support local file type env set
		if url != nil && url.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			modelRealVersion, err := getDeployedModelRealVersion(modelRegistry, endpoint.Spec.Model.Name, endpoint.Spec.Model.Version)
			if err != nil {
				return dashboard.RayServeApplication{}, errors.Wrapf(err, "failed to get real model version for model %s", endpoint.Spec.Model.Name)
			}

			// bentoml model registry path: <BENTOML_HOME>/models/<model_name>/<model_version>
			// so we need to append "models" to the path
			modelArgs["registry_path"] = filepath.Join("/mnt", endpoint.Metadata.Workspace, endpoint.Metadata.Name, "models", endpoint.Spec.Model.Name, modelRealVersion)
			modelArgs["path"] = filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, modelCacheRelativePath, endpoint.Spec.Model.Name, modelRealVersion)
		}
	case v1.HuggingFaceModelRegistryType:
		applicationEnv[v1.HFEndpoint] = strings.TrimSuffix(modelRegistry.Spec.Url, "/")
		if modelRegistry.Spec.Credentials != "" {
			applicationEnv[v1.HFTokenEnv] = modelRegistry.Spec.Credentials
		}

		modelArgs["registry_path"] = endpoint.Spec.Model.Name
		modelArgs["path"] = filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, modelCacheRelativePath, endpoint.Spec.Model.Name)
	}

	app.Args["model"] = modelArgs

	maps.Copy(app.Args, endpoint.Spec.Variables)

	app.RuntimeEnv = map[string]interface{}{
		"env_vars": applicationEnv,
	}

	return app, nil
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
