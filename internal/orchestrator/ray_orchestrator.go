package orchestrator

import (
	"context"
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

	storage            storage.Storage
	acceleratorManager accelerator.Manager
}

type RayOptions struct {
	Options
}

func NewRayOrchestrator(opts RayOptions) *RayOrchestrator {
	o := &RayOrchestrator{
		cluster:            opts.Cluster,
		storage:            opts.Storage,
		acceleratorManager: opts.AcceleratorManager,
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
	// pre-check related resources
	_, err := getEndpointDeployCluster(o.storage, endpoint)
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

	newApp, err := EndpointToApplication(endpoint, modelRegistry, o.acceleratorManager)
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

	status, exists := currentAppsResp.Applications[EndpointToServeApplicationName(endpoint)]
	if !exists {
		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhasePENDING,
			ErrorMessage: "Endpoint not found in Ray Serve applications",
		}, nil
	}

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
func EndpointToApplication(endpoint *v1.Endpoint, modelRegistry *v1.ModelRegistry, acceleratorManager accelerator.Manager) (dashboard.RayServeApplication, error) {
	rayResource, err := acceleratorManager.ConvertToRay(context.Background(), endpoint.Spec.Resources)
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

	args := map[string]interface{}{
		"model": map[string]interface{}{
			"registry_type": modelRegistry.Spec.Type,
			"name":          endpoint.Spec.Model.Name,
			"file":          endpoint.Spec.Model.File,
			"version":       endpoint.Spec.Model.Version,
			"task":          endpoint.Spec.Model.Task,
		},
		"deployment_options": endpoint.Spec.DeploymentOptions,
	}

	maps.Copy(args, endpoint.Spec.Variables)

	app := dashboard.RayServeApplication{
		Name:        EndpointToServeApplicationName(endpoint),
		RoutePrefix: fmt.Sprintf("/%s/%s", endpoint.Metadata.Workspace, endpoint.Metadata.Name),
		ImportPath:  fmt.Sprintf("serve.%s.%s.app:app_builder", strings.ReplaceAll(endpoint.Spec.Engine.Engine, "-", "_"), endpoint.Spec.Engine.Version),
		Args:        args,
	}

	applicationEnv := map[string]string{}

	for k, v := range endpoint.Spec.Env {
		applicationEnv[k] = v
	}

	switch modelRegistry.Spec.Type {
	case v1.BentoMLModelRegistryType:
		url, _ := url.Parse(modelRegistry.Spec.Url) // nolint: errcheck
		// todo: support local file type env set
		if url != nil && url.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			applicationEnv[v1.BentoMLHomeEnv] = filepath.Join("/mnt", endpoint.Key(), modelRegistry.Key(), endpoint.Spec.Model.Name)
		}
	case v1.HuggingFaceModelRegistryType:
		applicationEnv[v1.HFEndpoint] = strings.TrimSuffix(modelRegistry.Spec.Url, "/")
		if modelRegistry.Spec.Credentials != "" {
			applicationEnv[v1.HFTokenEnv] = modelRegistry.Spec.Credentials
		}
	}

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
