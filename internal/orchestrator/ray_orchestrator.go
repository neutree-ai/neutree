package orchestrator

import (
	"context"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/cluster"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var _ Orchestrator = &RayOrchestrator{}

var (
	ErrClusterHealthCheck = errors.New("cluster health check failed")
	ErrorRayNodeNotFound  = errors.New("ray node not found")
)

type OperationConfig struct {
	UpTimeout        time.Duration
	DownTimeout      time.Duration
	StartNodeTimeout time.Duration
	StopNodeTimeout  time.Duration
	DrainNodeTimeout time.Duration
	CommonTimeout    time.Duration
}

type RayOrchestrator struct {
	cluster *v1.Cluster

	storage       storage.Storage
	clusterHelper cluster.ClusterManager
	opTimeout     OperationConfig
}

type RayOptions struct {
	Options
	clusterManager cluster.ClusterManager
}

func (o *RayOrchestrator) CreateCluster() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.UpTimeout)
	defer cancel()

	if o.cluster.IsInitialized() {
		// if cluster already initialized, but still need to create cluster, may need to restart head node.
		return o.clusterHelper.UpCluster(ctx, true)
	} else {
		return o.clusterHelper.UpCluster(ctx, false)
	}
}

func (o *RayOrchestrator) DeleteCluster() error {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.DownTimeout)
	defer cancel()

	// down ray cluster
	err := o.clusterHelper.DownCluster(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to down ray cluster")
	}

	return nil
}

func (o *RayOrchestrator) SyncCluster() error {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.CommonTimeout)
	defer cancel()

	err := o.clusterHelper.Sync(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to sync ray cluster")
	}

	return nil
}

func (o *RayOrchestrator) HealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.CommonTimeout)
	defer cancel()

	dashboardService, err := o.getDashboardService(ctx)
	if err != nil {
		return errors.Wrap(ErrClusterHealthCheck, err.Error())
	}

	// check ray cluster health by get cluster metadata.
	_, err = dashboardService.GetClusterMetadata()
	if err != nil {
		return errors.Wrap(ErrClusterHealthCheck, err.Error())
	}

	return nil
}

func (o *RayOrchestrator) StartNode(nodeIP string) error {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.StartNodeTimeout)
	defer cancel()

	if nodeIP == "" {
		return errors.New("node IP cannot be empty")
	}

	return o.clusterHelper.StartNode(ctx, nodeIP)
}

func (o *RayOrchestrator) StopNode(nodeIP string) error {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.StopNodeTimeout)
	defer cancel()

	return o.clusterHelper.StopNode(ctx, nodeIP)
}

func (o *RayOrchestrator) ListNodes() ([]v1.NodeSummary, error) {
	dashboardService, err := o.getDashboardService(context.Background())
	if err != nil {
		return nil, errors.Wrap(err, "failed to get dashboard service")
	}

	return dashboardService.ListNodes()
}

func (o *RayOrchestrator) GetDesireStaticWorkersIP() []string {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.CommonTimeout)
	defer cancel()

	return o.clusterHelper.GetDesireStaticWorkersIP(ctx)
}

func (o *RayOrchestrator) ClusterStatus() (*v1.RayClusterStatus, error) {
	clusterStatus := &v1.RayClusterStatus{}

	dashboardService, err := o.getDashboardService(context.Background())
	if err != nil {
		return nil, errors.Wrap(err, "failed to get dashboard service")
	}

	nodes, err := dashboardService.ListNodes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get ray nodes")
	}

	var (
		readyNodes            int
		neutreeServingVersion string
	)

	for _, node := range nodes {
		if !node.Raylet.IsHeadNode && node.Raylet.State == v1.AliveNodeState {
			readyNodes++
		}

		if _, ok := node.Raylet.Labels[v1.NeutreeServingVersionLabel]; !ok {
			continue
		}

		if neutreeServingVersion == "" {
			neutreeServingVersion = node.Raylet.Labels[v1.NeutreeServingVersionLabel]
		} else {
			var less bool

			less, err = semver.LessThan(neutreeServingVersion, node.Raylet.Labels[v1.NeutreeServingVersionLabel])
			if err != nil {
				return nil, errors.Wrap(err, "failed to compare neutree serving version")
			}

			if less {
				neutreeServingVersion = node.Raylet.Labels[v1.NeutreeServingVersionLabel]
			}
		}
	}

	clusterStatus.ReadyNodes = readyNodes
	clusterStatus.NeutreeServeVersion = neutreeServingVersion

	autoScaleStatus, err := dashboardService.GetClusterAutoScaleStatus()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cluster autoScale status")
	}

	var (
		currentAutoScaleActiveNodes int
		pendingLauncherNodes        int
	)

	for key, activeNodeNumber := range autoScaleStatus.ActiveNodes {
		// skip calculate headgroup active nodes.
		if key == "headgroup" || key == "local.cluster.node" {
			continue
		}

		currentAutoScaleActiveNodes += activeNodeNumber
	}

	for _, pendingLauncherNumber := range autoScaleStatus.PendingLaunches {
		pendingLauncherNodes += pendingLauncherNumber
	}

	clusterStatus.AutoScaleStatus.PendingNodes = len(autoScaleStatus.PendingNodes) + pendingLauncherNodes
	clusterStatus.AutoScaleStatus.ActiveNodes = currentAutoScaleActiveNodes
	clusterStatus.AutoScaleStatus.FailedNodes = len(autoScaleStatus.FailedNodes)

	clusterMetadata, err := dashboardService.GetClusterMetadata()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cluster metadata")
	}

	clusterStatus.PythonVersion = clusterMetadata.Data.PythonVersion
	clusterStatus.RayVersion = clusterMetadata.Data.RayVersion
	clusterStatus.DesireNodes = len(o.clusterHelper.GetDesireStaticWorkersIP(context.Background())) + clusterStatus.AutoScaleStatus.PendingNodes +
		clusterStatus.AutoScaleStatus.ActiveNodes + clusterStatus.AutoScaleStatus.FailedNodes

	return clusterStatus, nil
}

func NewRayOrchestrator(opts RayOptions) (*RayOrchestrator, error) {
	o := &RayOrchestrator{
		cluster: opts.Cluster,
		storage: opts.Storage,
		opTimeout: OperationConfig{
			UpTimeout:        time.Minute * 30,
			DownTimeout:      time.Minute * 30,
			StartNodeTimeout: time.Minute * 10,
			StopNodeTimeout:  time.Minute * 2,
			DrainNodeTimeout: time.Minute * 5,
			CommonTimeout:    time.Minute * 10,
		},
		clusterHelper: opts.clusterManager,
	}

	return o, nil
}

func (o *RayOrchestrator) getDashboardService(ctx context.Context) (dashboard.DashboardService, error) {
	return o.clusterHelper.GetDashboardService(ctx)
}

// CreateEndpoint deploys a new endpoint using Ray Serve.
func (o *RayOrchestrator) CreateEndpoint(endpoint *v1.Endpoint) (*v1.EndpointStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.CommonTimeout)
	defer cancel()

	// pre-check related resources
	cluster, err := o.storage.ListCluster(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Spec.Cluster),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Metadata.Workspace),
			},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list cluster")
	}

	if len(cluster) == 0 {
		return nil, errors.New("cluster " + endpoint.Spec.Cluster + " not found")
	}

	engine, err := o.storage.ListEngine(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Spec.Engine.Engine),
			},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list engine")
	}

	if len(engine) == 0 {
		return nil, errors.New("engine " + endpoint.Spec.Engine.Engine + " not found")
	}

	if engine[0].Status == nil || engine[0].Status.Phase != v1.EnginePhaseCreated {
		return nil, errors.New("engine " + endpoint.Spec.Engine.Engine + " not ready")
	}

	versionMatched := false

	for _, v := range engine[0].Spec.Versions {
		if v.Version == endpoint.Spec.Engine.Version {
			versionMatched = true
			break
		}
	}

	if !versionMatched {
		return nil, errors.New("engine " + endpoint.Spec.Engine.Engine + " version " + endpoint.Spec.Engine.Version + " not found")
	}

	modelRegistry, err := o.storage.ListModelRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Spec.Model.Registry),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Metadata.Workspace),
			},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list model registry")
	}

	if len(modelRegistry) == 0 {
		return nil, errors.New("model registry " + endpoint.Spec.Model.Registry + " not found")
	}

	if modelRegistry[0].Status == nil || modelRegistry[0].Status.Phase != v1.ModelRegistryPhaseCONNECTED {
		return nil, errors.New("model registry " + endpoint.Spec.Model.Registry + " not ready")
	}

	// call ray dashboard API
	dashboardService, err := o.getDashboardService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get dashboard service for creating endpoint")
	}

	currentAppsResp, err := dashboardService.GetServeApplications()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get current serve applications")
	}

	newApp := dashboard.EndpointToApplication(endpoint, &modelRegistry[0])

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
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.CommonTimeout)
	defer cancel()

	// pre-check cluster
	cluster, err := o.storage.ListCluster(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Spec.Cluster),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Metadata.Workspace),
			},
		},
	})
	if err != nil {
		return errors.Wrap(err, "failed to list cluster")
	}

	if len(cluster) == 0 {
		// it's safe to ignore this, because the cluster has been removed
		return nil
	}

	dashboardService, err := o.getDashboardService(ctx)
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
		if name == dashboard.EndpointToServeApplicationName(endpoint) {
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
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.CommonTimeout)
	defer cancel()

	// Placeholder implementation: Get all apps and check if ours exists.
	// A more robust implementation would query the specific app status if the API supports it,
	// or parse the status field from the GetServeApplications response.
	dashboardService, err := o.getDashboardService(ctx)
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

	status, exists := currentAppsResp.Applications[dashboard.EndpointToServeApplicationName(endpoint)]
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
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.CommonTimeout)
	defer cancel()

	modelRegistry, err := o.storage.ListModelRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Spec.Model.Registry),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Metadata.Workspace),
			},
		},
	})
	if err != nil {
		return errors.Wrap(err, "failed to list model registry")
	}

	if len(modelRegistry) == 0 {
		return errors.New("model registry " + endpoint.Spec.Model.Registry + " not found")
	}

	if modelRegistry[0].Status == nil || modelRegistry[0].Status.Phase != v1.ModelRegistryPhaseCONNECTED {
		return errors.New("model registry " + endpoint.Spec.Model.Registry + " not ready")
	}

	err = o.clusterHelper.ConnectEndpointModel(ctx, modelRegistry[0], *endpoint)
	if err != nil {
		return errors.Wrap(err, "failed to connect model")
	}

	return nil
}

func (o *RayOrchestrator) DisconnectEndpointModel(endpoint *v1.Endpoint) error {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.CommonTimeout)
	defer cancel()

	modelRegistry, err := o.storage.ListModelRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Spec.Model.Registry),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Metadata.Workspace),
			},
		},
	})

	if err != nil {
		return errors.Wrap(err, "failed to list model registry")
	}

	if len(modelRegistry) == 0 {
		return errors.New("model registry " + endpoint.Spec.Model.Registry + " not found")
	}

	err = o.clusterHelper.DisconnectEndpointModel(ctx, modelRegistry[0], *endpoint)
	if err != nil {
		return errors.Wrap(err, "failed to connect model")
	}

	return nil
}
