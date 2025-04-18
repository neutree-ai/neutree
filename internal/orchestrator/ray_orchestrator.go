package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var _ Orchestrator = &RayOrchestrator{}

var (
	ErrImageNotFound      = errors.New("image not found")
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
	config        *v1.RayClusterConfig
	cluster       *v1.Cluster
	imageRegistry *v1.ImageRegistry
	imageService  registry.ImageService

	clusterHelper ray.ClusterManager
	storage       storage.Storage
	opTimeout     OperationConfig
}

func (o *RayOrchestrator) CreateCluster() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.UpTimeout)
	defer cancel()

	err := o.checkDockerImage(o.config.Docker.Image)
	if err != nil {
		return "", errors.Wrap(err, "check ray cluster serving image failed")
	}

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
	// first stop all static node
	eg := &errgroup.Group{}

	for i := range o.config.Provider.WorkerIPs {
		nodeIP := o.config.Provider.WorkerIPs[i]

		eg.Go(func() error {
			return o.clusterHelper.StopNode(ctx, nodeIP)
		})
	}

	// best effort to stop node, ignore error.
	eg.Wait() //nolint:errcheck

	// down ray cluster
	err := o.clusterHelper.DownCluster(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to down ray cluster")
	}

	// remove local cluster state file to avoid ray cluster start failed.
	localClusterStatePath := fmt.Sprintf("%s/cluster-%s.state", getRayTmpDir(), o.config.ClusterName)
	if err = os.Remove(localClusterStatePath); err != nil {
		return errors.Wrap(err, "failed to remove local cluster state file")
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

	err := o.checkDockerImage(o.config.Docker.Image)
	if err != nil {
		return errors.Wrap(err, "check ray cluster serving image failed")
	}

	return o.clusterHelper.StartNode(ctx, nodeIP)
}

func (o *RayOrchestrator) StopNode(nodeIP string) error {
	ctx, cancel := context.WithTimeout(context.Background(), o.opTimeout.StopNodeTimeout)
	defer cancel()

	node, err := o.getNodeByIP(ctx, nodeIP)
	if err != nil {
		// no need to stop node if node not found.
		if err == ErrorRayNodeNotFound {
			return nil
		} else {
			return errors.Wrap(err, "failed to get node ID")
		}
	}

	if node.Raylet.State == v1.AliveNodeState {
		// current drainNode behavior is similar to ray stop, and the ray community will optimize it later.
		err = o.clusterHelper.DrainNode(ctx, node.Raylet.NodeID, "DRAIN_NODE_REASON_PREEMPTION", "stop node", 600)
		if err != nil {
			return errors.Wrap(err, "failed to drain node "+nodeIP)
		}
	}

	return o.clusterHelper.StopNode(ctx, nodeIP)
}

func (o *RayOrchestrator) getNodeByIP(_ context.Context, nodeIP string) (*v1.NodeSummary, error) {
	rayNodes, err := o.ListNodes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list ray nodes")
	}

	for i := range rayNodes {
		if rayNodes[i].IP == nodeIP {
			return &rayNodes[i], nil
		}
	}

	return nil, ErrorRayNodeNotFound
}

func (o *RayOrchestrator) ListNodes() ([]v1.NodeSummary, error) {
	dashboardService, err := o.getDashboardService(context.Background())
	if err != nil {
		return nil, errors.Wrap(err, "failed to get dashboard service")
	}

	return dashboardService.ListNodes()
}

func (o *RayOrchestrator) GetDesireStaticWorkersIP() []string {
	return o.config.Provider.WorkerIPs
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

	for _, activeNodeNumber := range autoScaleStatus.ActiveNodes {
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

	return clusterStatus, nil
}

func NewRayOrchestrator(opts Options) (*RayOrchestrator, error) {
	rayClusterConfig := &v1.RayClusterConfig{}

	rayConfig, err := json.Marshal(opts.Cluster.Spec.Config)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(rayConfig, rayClusterConfig)
	if err != nil {
		return nil, err
	}

	o := &RayOrchestrator{
		cluster:       opts.Cluster,
		imageRegistry: opts.ImageRegistry,
		imageService:  opts.ImageService,
		storage:       opts.Storage,
		config:        rayClusterConfig,
		opTimeout: OperationConfig{
			UpTimeout:        time.Minute * 30,
			DownTimeout:      time.Minute * 30,
			StartNodeTimeout: time.Minute * 10,
			StopNodeTimeout:  time.Minute * 2,
			DrainNodeTimeout: time.Minute * 5,
			CommonTimeout:    time.Minute * 1,
		},
	}

	err = o.setDefaultRayClusterConfig()
	if err != nil {
		return nil, err
	}

	o.clusterHelper, err = ray.NewRayClusterManager(o.config, &command.OSExecutor{})
	if err != nil {
		return nil, err
	}

	if opts.Cluster.IsInitialized() {
		if o.config.Provider.Type == "local" {
			// when ray provider is local, all cluster operation depend on local provider cluster state file.
			// So we need to ensure local provider cache exists after cluster initialized.
			err = o.ensureLocalClusterStateFile()
			if err != nil {
				return nil, errors.Wrap(err, "failed to ensure local cluster state file")
			}
		}
	}

	return o, nil
}

func (o *RayOrchestrator) ensureLocalClusterStateFile() error {
	rayClusterTmpDir := getRayTmpDir()

	err := os.MkdirAll(rayClusterTmpDir, 0700)
	if err != nil {
		return err
	}

	localClusterStatePath := fmt.Sprintf("%s/cluster-%s.state", rayClusterTmpDir, o.config.ClusterName)
	if _, err = os.Stat(localClusterStatePath); err == nil {
		return nil
	}
	// create local cluster state file
	localClusterState := map[string]v1.LocalNodeStatus{}
	localClusterState[o.config.Provider.HeadIP] = v1.LocalNodeStatus{
		Tags: map[string]string{
			"ray-node-type":   "head",
			"ray-node-status": "up-to-date",
		},
		State: "running",
	}

	localClusterStateContent, err := json.Marshal(localClusterState)
	if err != nil {
		return err
	}

	err = os.WriteFile(fmt.Sprintf("%s/cluster-%s.state", rayClusterTmpDir, o.config.ClusterName), localClusterStateContent, 0600)
	if err != nil {
		return err
	}

	return nil
}

// setDefaultRayClusterConfig set default ray cluster config.
func (o *RayOrchestrator) setDefaultRayClusterConfig() error {
	o.config.ClusterName = o.cluster.Metadata.Name
	// current only support local provider
	if o.config.Provider.Type == "" {
		o.config.Provider.Type = "local"
	}

	if o.config.Docker.ContainerName == "" {
		o.config.Docker.ContainerName = "ray_container"
	}

	registryURL, err := url.Parse(o.imageRegistry.Spec.URL)
	if err != nil {
		return errors.Wrap(err, "failed to parse image registry url "+o.imageRegistry.Spec.URL)
	}

	o.config.Docker.Image = registryURL.Host + "/" + o.imageRegistry.Spec.Repository + "/neutree-serve:" + o.cluster.Spec.Version
	o.config.Docker.PullBeforeRun = true
	o.config.HeadStartRayCommands = []string{
		"ray stop",
		fmt.Sprintf(`ray start --disable-usage-stats --head --metrics-export-port=%d --port=6379 --object-manager-port=8076 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"%s":"%s"}'`, //nolint:lll
			v1.RayletMetricsPort, v1.NeutreeServingVersionLabel, o.cluster.Spec.Version),
	}
	o.config.WorkerStartRayCommands = []string{
		"ray stop",
		fmt.Sprintf(`python /home/ray/start.py $RAY_HEAD_IP --metrics-export-port=%d --disable-usage-stats --labels='{"%s":"%s","%s":"%s"}'`,
			v1.RayletMetricsPort, v1.NeutreeNodeProvisionTypeLabel, v1.AutoScaleNodeProvisionType, v1.NeutreeServingVersionLabel, o.cluster.Spec.Version),
	}
	o.config.StaticWorkerStartRayCommands = []string{
		"ray stop",
		fmt.Sprintf(`python /home/ray/start.py $RAY_HEAD_IP --metrics-export-port=%d --disable-usage-stats --labels='{"%s":"%s","%s":"%s"}'`,
			v1.RayletMetricsPort, v1.NeutreeNodeProvisionTypeLabel, v1.StaticNodeProvisionType, v1.NeutreeServingVersionLabel, o.cluster.Spec.Version),
	}

	initializationCommands := []string{}
	// set registry CA.
	if o.imageRegistry.Spec.Ca != "" {
		certPath := filepath.Join("/etc/docker/certs.d", registryURL.Host)
		initializationCommands = append(initializationCommands, "mkdir -p "+certPath)
		initializationCommands = append(initializationCommands, fmt.Sprintf(`echo "%s" | base64 -d > %s/ca.crt`, o.imageRegistry.Spec.Ca, certPath))
	}

	// login registry.
	dockerLoginCommand := "docker login " + registryURL.Host + " -u " + o.imageRegistry.Spec.AuthConfig.Username + " -p "

	switch {
	case o.imageRegistry.Spec.AuthConfig.Password != "":
		dockerLoginCommand += o.imageRegistry.Spec.AuthConfig.Password
	case o.imageRegistry.Spec.AuthConfig.IdentityToken != "":
		dockerLoginCommand += o.imageRegistry.Spec.AuthConfig.IdentityToken
	case o.imageRegistry.Spec.AuthConfig.RegistryToken != "":
		dockerLoginCommand += o.imageRegistry.Spec.AuthConfig.RegistryToken
	}

	initializationCommands = append(initializationCommands, dockerLoginCommand)
	o.config.InitializationCommands = initializationCommands

	return nil
}

func (o *RayOrchestrator) checkDockerImage(image string) error {
	if o.imageRegistry.Status == nil || o.imageRegistry.Status.Phase != v1.ImageRegistryPhaseCONNECTED {
		return errors.New("image registry " + o.imageRegistry.Metadata.Name + " not connected")
	}

	imageExisted, err := o.imageService.CheckImageExists(image, authn.FromConfig(authn.AuthConfig{
		Username:      o.imageRegistry.Spec.AuthConfig.Username,
		Password:      o.imageRegistry.Spec.AuthConfig.Password,
		Auth:          o.imageRegistry.Spec.AuthConfig.Auth,
		IdentityToken: o.imageRegistry.Spec.AuthConfig.IdentityToken,
		RegistryToken: o.imageRegistry.Spec.AuthConfig.IdentityToken,
	}))

	if err != nil {
		return err
	}

	if !imageExisted {
		return errors.Wrap(ErrImageNotFound, "image "+o.config.Docker.Image+" not found")
	}

	return nil
}

func (o *RayOrchestrator) getDashboardService(ctx context.Context) (dashboard.DashboardService, error) {
	var dashboardService dashboard.DashboardService

	if o.cluster.IsInitialized() {
		dashboardService = dashboard.NewDashboardService(o.cluster.Status.DashboardURL)
	} else {
		headIP, err := o.clusterHelper.GetHeadIP(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get head ip")
		}

		dashboardService = dashboard.NewDashboardService(fmt.Sprintf("http://%s:8265", headIP))
	}

	return dashboardService, nil
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
				Value:    endpoint.Spec.Cluster,
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
				Value:    endpoint.Spec.Engine.Engine,
			},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list engine")
	}
	if len(engine) == 0 {
		return nil, errors.New("engine " + endpoint.Spec.Engine.Engine + " not found")
	}
	if engine[0].Status.Phase != v1.EnginePhaseCreated {
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
				Value:    endpoint.Spec.Model.Registry,
			},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list model registry")
	}
	if len(modelRegistry) == 0 {
		return nil, errors.New("model registry " + endpoint.Spec.Model.Registry + " not found")
	}
	if modelRegistry[0].Status.Phase != v1.ModelRegistryPhaseCONNECTED {
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
	updatedAppsList := make([]dashboard.RayServeApplication, 0, len(currentAppsResp.Applications)+1)
	for _, appStatus := range currentAppsResp.Applications {
		updatedAppsList = append(updatedAppsList, *appStatus.DeployedAppConfig)
	}
	updatedAppsList = append(updatedAppsList, newApp)

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

	serviceURL, err := dashboard.FormatServiceURL(o.cluster, endpoint)
	if err != nil {
		// Log the error, but the endpoint might still be running
		klog.Warningf("Warning: failed to format service URL: %v", err)
	}

	return &v1.EndpointStatus{
		Phase:        v1.EndpointPhaseRUNNING,
		ServiceURL:   serviceURL,
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
				Value:    endpoint.Spec.Cluster,
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
		if name == endpoint.Metadata.Name {
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

	status, exists := currentAppsResp.Applications[endpoint.Metadata.Name]
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
	case "RUNNING":
	case "DELETING":
		phase = v1.EndpointPhaseRUNNING
	case "NOT_STARTED":
	case "DEPLOYING":
		phase = v1.EndpointPhasePENDING
	case "DEPLOY_FAILED":
	case "UNHEALTHY":
		phase = v1.EndpointPhaseFAILED
	default:
		phase = v1.EndpointPhaseFAILED
	}

	serviceURL, err := dashboard.FormatServiceURL(o.cluster, endpoint)
	if err != nil {
		fmt.Printf("Warning: failed to format service URL while getting status: %v\n", err)
	}

	return &v1.EndpointStatus{
		Phase:        phase,
		ServiceURL:   serviceURL,
		ErrorMessage: status.Message, // Use message from Ray if available
	}, nil
}

func getRayTmpDir() string {
	tmpDir := "/tmp"

	if os.Getenv("RAY_TMP_DIR") != "" {
		tmpDir = os.Getenv("RAY_TMP_DIR")
	}

	return filepath.Join(tmpDir, "ray")
}
