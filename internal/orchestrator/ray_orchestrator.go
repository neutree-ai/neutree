package orchestrator

import (
	"fmt"
	"maps"
	"math"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/model_registry"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// isNewClusterVersion returns true if the cluster version is > v1.0.0.
// Returns an error if the cluster version cannot be parsed as semver.
func isNewClusterVersion(cluster *v1.Cluster) (bool, error) {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.Version == "" {
		klog.Warningf("cluster version is empty, treating as old cluster (<= v1.0.0)")
		return false, nil
	}

	isNew, err := semver.LessThan("v1.0.0", cluster.Spec.Version)
	if err != nil {
		return false, errors.Errorf("failed to parse cluster version %q: %v", cluster.Spec.Version, err)
	}

	return isNew, nil
}

// clusterLocks provides per-cluster mutexes to serialize Ray Serve application
// updates (read-modify-write on PUT /api/serve/applications/) and prevent
// concurrent workers from overwriting each other's changes.
var clusterLocks sync.Map

const (
	modelDownloadLogTailLines = 200

	modelDownloadStartMarker  = "NEUTREE_MODEL_DOWNLOAD_START"
	modelDownloadDoneMarker   = "NEUTREE_MODEL_DOWNLOAD_DONE"
	modelDownloadFailedMarker = "NEUTREE_MODEL_DOWNLOAD_FAILED"

	rayAppStatusDeploying    = "DEPLOYING"
	rayAppStatusNotStarted   = "NOT_STARTED"
	rayAppStatusDeployFailed = "DEPLOY_FAILED"
	rayAppStatusUnhealthy    = "UNHEALTHY"
	rayAppStatusRunning      = "RUNNING"
)

type modelDownloadMarkerState int

const (
	modelDownloadMarkerNone modelDownloadMarkerState = iota
	modelDownloadMarkerInProgress
	modelDownloadMarkerDone
	modelDownloadMarkerFailed
)

func getClusterLock(clusterKey string) *sync.Mutex {
	actual, _ := clusterLocks.LoadOrStore(clusterKey, &sync.Mutex{})
	return actual.(*sync.Mutex) //nolint:errcheck // type is guaranteed by LoadOrStore
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
	isNew, err := isNewClusterVersion(ctx.Cluster)
	if err != nil {
		return err
	}

	if !isNew {
		// always exec connect model to cluster, for cluster may dynamic scale, we need ensure model exists on all cluster nodes.
		// todo: In order to reduce model connection actions, a new controller may be created in the future to uniformly manage model connections on the cluster.
		err = o.connectSSHClusterEndpointModel(*ctx.ModelRegistry, *ctx.Endpoint, connect)
		if err != nil {
			return errors.Wrapf(err, "failed to connect model registry before creating endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
		}
	}

	return o.createOrUpdate(ctx)
}

// PauseEndpoint removes the endpoint's Ray Serve application, which is how
// SSH/Ray clusters represent "scaled to zero". Idempotent: missing app => no-op.
//
// Pause does not need ModelRegistry/Engine/ImageRegistry; only the deploy
// cluster (for the dashboard URL) is fetched. The inner deleteEndpoint(ctx)
// is reused because it depends only on cluster + endpoint + rayService.
func (o *RayOrchestrator) PauseEndpoint(endpoint *v1.Endpoint) error {
	ctx, err := o.prepareOrchestratorContextForPauseDelete(endpoint)
	if err != nil {
		return errors.Wrapf(err, "failed to prepare context for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	ctx.logger.V(4).Info("Pausing endpoint by deleting from Ray Serve")

	if err := o.deleteEndpoint(ctx); err != nil {
		return errors.Wrapf(err, "failed to pause endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	return nil
}

// prepareOrchestratorContextForPauseDelete is the pause/delete equivalent of
// prepareOrchestratorContext: it fetches only what those operations actually
// need (deploy cluster + dashboard service) and skips
// ModelRegistry/Engine/ImageRegistry lookups so a removed model registry does
// not block convergence to Paused/Deleted.
func (o *RayOrchestrator) prepareOrchestratorContextForPauseDelete(endpoint *v1.Endpoint) (*OrchestratorContext, error) {
	deployedCluster, err := getEndpointDeployCluster(o.storage, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get deploy cluster")
	}

	if deployedCluster.Status == nil || deployedCluster.Status.DashboardURL == "" {
		return nil, errors.New("dashboard URL is not configured in cluster status")
	}

	return &OrchestratorContext{
		Cluster:    deployedCluster,
		Endpoint:   endpoint,
		rayService: dashboard.NewDashboardService(deployedCluster.Status.DashboardURL),
		logger:     klog.LoggerWithValues(klog.Background(), "endpoint", endpoint.Metadata.WorkspaceName()),
	}, nil
}

func (o *RayOrchestrator) createOrUpdate(ctx *OrchestratorContext) error {
	mu := getClusterLock(ctx.Cluster.Metadata.WorkspaceName())
	mu.Lock()
	defer mu.Unlock()

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
//
// Delete does not need ModelRegistry/Engine/ImageRegistry on new clusters
// (>v1.0.0). On old SSH clusters (<=v1.0.0) the NFS mount is still
// disconnected, but ErrResourceNotFound on the registry lookup is tolerated:
// if the registry has already been removed the SSH-side mount is orphaned
// and continuing the delete is the correct outcome.
func (o *RayOrchestrator) DeleteEndpoint(endpoint *v1.Endpoint) error {
	ctx, err := o.prepareOrchestratorContextForPauseDelete(endpoint)
	if err != nil {
		return errors.Wrapf(err, "failed to prepare context for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	// For clusters <= v1.0.0, NFS is mounted inside ray_container via SSH — need to disconnect.
	// For clusters > v1.0.0, NFS is mounted via engine container run_options, so skip disconnect.
	isNew, err := isNewClusterVersion(ctx.Cluster)
	if err != nil {
		return err
	}

	if !isNew {
		modelRegistry, mErr := getEndpointModelRegistry(o.storage, endpoint)

		switch {
		case mErr == nil:
			if dErr := o.connectSSHClusterEndpointModel(*modelRegistry, *endpoint, disconnect); dErr != nil {
				return errors.Wrapf(dErr, "failed to disconnect model registry before deleting endpoint %s", endpoint.Metadata.WorkspaceName())
			}
		case errors.Is(mErr, storage.ErrResourceNotFound):
			ctx.logger.Info("Model registry already removed; skipping SSH-side NFS disconnect (best-effort)",
				"registry", endpoint.Spec.Model.Registry)
		default:
			return errors.Wrap(mErr, "failed to get model registry for endpoint disconnect")
		}
	}

	ctx.logger.V(4).Info("Deleting endpoint from Ray Serve")

	return o.deleteEndpoint(ctx)
}

func (o *RayOrchestrator) deleteEndpoint(ctx *OrchestratorContext) error {
	mu := getClusterLock(ctx.Cluster.Metadata.WorkspaceName())
	mu.Lock()
	defer mu.Unlock()

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

// allDeploymentsHealthy returns true when every entry in the deployment map
// reports HEALTHY. An empty map returns true; callers should guard with
// len(deployments) > 0 when "no deployments registered" should not count as
// healthy.
func allDeploymentsHealthy(deployments map[string]dashboard.Deployment) bool {
	for _, dep := range deployments {
		if dep.Status != dashboard.DeploymentStatusHealthy {
			return false
		}
	}

	return true
}

func allProxiesHealthy(proxies map[string]dashboard.ProxyStatus) bool {
	if len(proxies) == 0 {
		return false
	}

	for _, proxyStatus := range proxies {
		if proxyStatus.Status != dashboard.ProxyStatusHealthy {
			return false
		}
	}

	return true
}

// mapDeployingPhase resolves the endpoint phase when Ray Serve reports the
// application as DEPLOYING or NOT_STARTED. Returns Failed when any deployment
// is UNHEALTHY; returns Running when the previous Neutree phase was Running,
// the proxy is healthy, every deployment is HEALTHY, and the upstream app
// status is DEPLOYING — the false-positive window opened by Ray's PUT
// /api/serve/applications/ when it unconditionally writes DEPLOYING to every
// application in the request even if their configs are unchanged (see
// ray-project/ray#25381, #42974, #44226). NOT_STARTED is excluded from
// suppression: Ray returns it when the application is not present in its
// state, which never matches the false-positive window. Otherwise returns
// Deploying.
//
// The suppression is a snapshot heuristic — it accepts the case where
// Neutree misses an in-progress UPDATING window between polls (poll cadence
// > deployment-update duration). In that case the deployments observed are
// already HEALTHY and traffic is being served; reporting Running matches
// the actual serving state.
func mapDeployingPhase(endpoint *v1.Endpoint, status dashboard.RayServeApplicationStatus, proxyReady bool) v1.EndpointPhase {
	for _, deployment := range status.Deployments {
		if deployment.Status == dashboard.DeploymentStatusUnhealthy {
			return v1.EndpointPhaseFAILED
		}
	}

	if status.Status == rayAppStatusDeploying &&
		proxyReady &&
		endpoint.Status != nil &&
		endpoint.Status.Phase == v1.EndpointPhaseRUNNING &&
		len(status.Deployments) > 0 &&
		allDeploymentsHealthy(status.Deployments) {
		return v1.EndpointPhaseRUNNING
	}

	return v1.EndpointPhaseDEPLOYING
}

func modelDownloadStateFromLog(logText string) modelDownloadMarkerState {
	if strings.Contains(logText, modelDownloadFailedMarker) {
		return modelDownloadMarkerFailed
	}

	if strings.Contains(logText, modelDownloadDoneMarker) {
		return modelDownloadMarkerDone
	}

	if strings.Contains(logText, modelDownloadStartMarker) && !strings.Contains(logText, modelDownloadDoneMarker) {
		return modelDownloadMarkerInProgress
	}

	return modelDownloadMarkerNone
}

func inspectRayModelDownloadLogs(svc dashboard.DashboardService, appStatus dashboard.RayServeApplicationStatus) (modelDownloadMarkerState, string, error) {
	var (
		firstErr         error
		doneDetail       string
		inProgressDetail string
		unknownActorSeen bool
	)

	for deploymentKey, deployment := range appStatus.Deployments {
		deploymentName := deployment.Name
		if deploymentName == "" {
			deploymentName = deploymentKey
		}

		for _, replica := range deployment.Replicas {
			if replica.ActorID == "" {
				continue
			}

			replicaID := replica.ReplicaID
			if replicaID == "" {
				replicaID = replica.ActorID
			}

			replicaState := modelDownloadMarkerNone

			for _, suffix := range []string{"out", "err"} {
				logText, err := svc.GetActorLog(replica.ActorID, suffix, modelDownloadLogTailLines)
				if err != nil {
					if firstErr == nil {
						firstErr = errors.Wrapf(err, "failed to get %s log for actor %s", suffix, replica.ActorID)
					}

					continue
				}

				switch modelDownloadStateFromLog(logText) {
				case modelDownloadMarkerFailed:
					return modelDownloadMarkerFailed,
						fmt.Sprintf("Deployment %s replica %s model download failed", deploymentName, replicaID),
						nil
				case modelDownloadMarkerDone:
					replicaState = modelDownloadMarkerDone
				case modelDownloadMarkerInProgress:
					if replicaState != modelDownloadMarkerDone {
						replicaState = modelDownloadMarkerInProgress
					}
				}
			}

			switch replicaState {
			case modelDownloadMarkerDone:
				doneDetail = fmt.Sprintf("Deployment %s replica %s model download completed", deploymentName, replicaID)
			case modelDownloadMarkerInProgress:
				inProgressDetail = fmt.Sprintf("Deployment %s replica %s model download in progress", deploymentName, replicaID)
			case modelDownloadMarkerNone:
				unknownActorSeen = true
			}
		}
	}

	if inProgressDetail != "" {
		return modelDownloadMarkerInProgress, inProgressDetail, nil
	}

	if unknownActorSeen {
		return modelDownloadMarkerNone, "", firstErr
	}

	if doneDetail != "" {
		return modelDownloadMarkerDone, doneDetail, nil
	}

	return modelDownloadMarkerNone, "", firstErr
}

func currentModelDownloadCompleted(endpoint *v1.Endpoint, currentModelHash string) bool {
	if currentModelHash == "" || endpoint == nil || endpoint.Status == nil {
		return false
	}

	return endpoint.Status.ModelDownloadCompleted != nil &&
		*endpoint.Status.ModelDownloadCompleted &&
		endpoint.Status.ModelDownloadCompletedHash != nil &&
		*endpoint.Status.ModelDownloadCompletedHash == currentModelHash
}

func setModelDownloadStatus(status *v1.EndpointStatus, completed bool, currentModelHash string) {
	status.ModelDownloadCompleted = &completed

	if completed {
		status.ModelDownloadCompletedHash = &currentModelHash
		return
	}

	emptyHash := ""
	status.ModelDownloadCompletedHash = &emptyHash
}

func resolveRayStartupModelDownloadStatus(
	svc dashboard.DashboardService,
	appStatus dashboard.RayServeApplicationStatus,
	phase v1.EndpointPhase,
	modelDownloadCompleted bool,
) (v1.EndpointPhase, bool, bool, []string) {
	if phase != v1.EndpointPhaseDEPLOYING ||
		(appStatus.Status != rayAppStatusDeploying && appStatus.Status != rayAppStatusNotStarted) {
		return phase, modelDownloadCompleted, false, nil
	}

	var errorMessages []string
	modelDownloadIncomplete := false

	markerState, markerMsg, markerErr := inspectRayModelDownloadLogs(svc, appStatus)
	if markerErr != nil {
		klog.V(4).Info("failed to inspect Ray actor model download logs", "error", markerErr)
	}

	switch markerState {
	case modelDownloadMarkerFailed:
		phase = v1.EndpointPhaseFAILED
		modelDownloadIncomplete = true

		errorMessages = append(errorMessages, markerMsg)
	case modelDownloadMarkerDone:
		modelDownloadCompleted = true
	case modelDownloadMarkerInProgress:
		phase = v1.EndpointPhaseMODELDOWNLOADING
		modelDownloadIncomplete = true

		errorMessages = append(errorMessages, markerMsg)
	case modelDownloadMarkerNone:
		if !modelDownloadCompleted {
			phase = v1.EndpointPhaseMODELDOWNLOADING
			modelDownloadIncomplete = true

			errorMessages = append(errorMessages, "current model download is not completed")
		}
	}

	return phase, modelDownloadCompleted, modelDownloadIncomplete, errorMessages
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

	proxyReady := allProxiesHealthy(currentAppsResp.Proxies)

	currentModelHash, hashErr := util.ComputeEndpointModelHash(endpoint)
	if hashErr != nil {
		klog.Warningf("failed to compute endpoint %s model hash: %v", endpoint.Metadata.WorkspaceName(), hashErr)
	}

	modelDownloadCompleted := currentModelDownloadCompleted(endpoint, currentModelHash)
	modelDownloadIncomplete := false

	// Basic status mapping
	// https://docs.ray.io/en/latest/serve/api/doc/ray.serve.schema.ApplicationStatus.html#ray.serve.schema.ApplicationStatus
	var phase v1.EndpointPhase
	var errorMessages []string

	switch status.Status {
	case rayAppStatusDeploying, rayAppStatusNotStarted:
		phase = mapDeployingPhase(endpoint, status, proxyReady)
	case rayAppStatusDeployFailed, rayAppStatusUnhealthy:
		phase = v1.EndpointPhaseFAILED
	case rayAppStatusRunning:
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
		endpointStatus := &v1.EndpointStatus{
			Phase:        phase,
			ErrorMessage: "",
		}
		if currentModelHash != "" {
			setModelDownloadStatus(endpointStatus, true, currentModelHash)
		}

		return endpointStatus, nil
	}

	var modelDownloadMessages []string
	phase, modelDownloadCompleted, modelDownloadIncomplete, modelDownloadMessages =
		resolveRayStartupModelDownloadStatus(dashboardService, status, phase, modelDownloadCompleted)
	errorMessages = append(errorMessages, modelDownloadMessages...)

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
		case v1.EndpointPhaseMODELDOWNLOADING:
			errorMsg = "Endpoint model download in progress: " + errorMsg
		case v1.EndpointPhaseFAILED:
			errorMsg = "Endpoint failed: " + errorMsg
		}
	}

	endpointStatus := &v1.EndpointStatus{
		Phase:        phase,
		ErrorMessage: errorMsg, // Use merged error message
	}

	if currentModelHash != "" {
		if modelDownloadCompleted {
			setModelDownloadStatus(endpointStatus, true, currentModelHash)
		} else if modelDownloadIncomplete {
			setModelDownloadStatus(endpointStatus, false, currentModelHash)
		}
	}

	return endpointStatus, nil
}

// endpointToApplication converts Neutree Endpoint and ModelRegistry to a RayServeApplication.
func EndpointToApplication(endpoint *v1.Endpoint, deployedCluster *v1.Cluster,
	modelRegistry *v1.ModelRegistry, engine *v1.Engine, imageRegistry *v1.ImageRegistry,
	acceleratorMgr accelerator.Manager) (dashboard.RayServeApplication, error) {
	// Strip any variant/prerelease/build suffix (e.g., "-cu130", "-rocm5", "-beta", "+build123")
	// from the version for the import path. All such variants share the same serve app code
	// as the base version. Non-semver versions (e.g., "gemma4") are used as-is.
	baseVersion, err := semver.BaseVersion(endpoint.Spec.Engine.Version)
	if err != nil {
		klog.Warningf("engine version %q is not a semver string, using as-is for import path: %v",
			endpoint.Spec.Engine.Version, err)

		baseVersion = endpoint.Spec.Engine.Version
	}

	app := dashboard.RayServeApplication{
		Name:        EndpointToServeApplicationName(endpoint),
		RoutePrefix: fmt.Sprintf("/%s/%s", endpoint.Metadata.Workspace, endpoint.Metadata.Name),
		ImportPath: fmt.Sprintf("serve.%s.%s.app:app_builder", strings.ReplaceAll(endpoint.Spec.Engine.Engine, "-", "_"),
			strings.NewReplacer(".", "_", "-", "_").Replace(baseVersion)),
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

	setDefaultTensorParallelSize(endpoint, &app, rayResource.NumGPUs)

	setEngineSpecialEnv(endpoint, deployedCluster, applicationEnv)

	app.RuntimeEnv = map[string]interface{}{
		"env_vars": applicationEnv,
	}

	// Features gated on new cluster version (> v1.0.0):
	//   - ENGINE_NAME / ENGINE_VERSION env vars for metrics labeling
	//   - runtime_env.container for engine version isolation
	//
	// Old clusters (<= v1.0.0) run the engine inside ray_container directly and
	// never had these env vars; injecting them unconditionally would cause a
	// spurious serve-application diff during CP upgrades.
	//
	// Two container configs are produced for new clusters:
	//   - baseConfig → app.RuntimeEnv["container"]: engine image + --rm only,
	//     inherited by app_builder and Controller (no GPU required).
	//   - backendConfig → app.Args["backend_container"]: full config with GPU
	//     options, volume mounts, and NFS. The Python app_builder sets this on
	//     Backend's ray_actor_options.runtime_env.container to override the
	//     app-level config.
	isNewCluster, err := isNewClusterVersion(deployedCluster)
	if err != nil {
		return dashboard.RayServeApplication{}, err
	}

	if isNewCluster {
		// Inject engine identity for metrics labeling (used by NeutreeRayStatLogger / _SanitizedRayStatLogger).
		if endpoint.Spec.Engine != nil {
			applicationEnv["ENGINE_NAME"] = endpoint.Spec.Engine.Engine
			applicationEnv["ENGINE_VERSION"] = endpoint.Spec.Engine.Version
		}

		baseConfig, backendConfig, err := buildEngineContainerConfigs(endpoint, engine, imageRegistry, acceleratorMgr, modelCaches, modelRegistry)
		if err != nil {
			return dashboard.RayServeApplication{}, errors.Wrapf(err, "failed to build engine container config for endpoint %s", endpoint.Metadata.WorkspaceName())
		}

		if baseConfig != nil {
			app.RuntimeEnv["container"] = baseConfig
		}

		if backendConfig != nil {
			app.Args["backend_container"] = backendConfig
		}
	}

	return app, nil
}

// setDefaultTensorParallelSize auto-sets the tensor-parallel field in
// engine_args to GPU count when GPU > 1 and is a whole number. Skips if the
// engine doesn't take a TP arg or if the user already configured it (in
// either underscore or kebab form — users sometimes copy CLI flag names
// verbatim).
func setDefaultTensorParallelSize(endpoint *v1.Endpoint, app *dashboard.RayServeApplication, numGPUs float64) {
	if endpoint.Spec.Engine == nil {
		return
	}

	tpKey := engineTPArgKey(endpoint.Spec.Engine.Engine)
	if tpKey == "" {
		return
	}

	if numGPUs <= 1 || math.Trunc(numGPUs) != numGPUs {
		return
	}

	engineArgs, ok := app.Args["engine_args"].(map[string]interface{})
	if !ok {
		engineArgs = make(map[string]interface{})
	}

	if _, hasUnderscore := engineArgs[tpKey]; hasUnderscore {
		return
	}

	dashKey := strings.ReplaceAll(tpKey, "_", "-")
	if _, hasDash := engineArgs[dashKey]; hasDash {
		return
	}

	engineArgs[tpKey] = int(numGPUs)
	app.Args["engine_args"] = engineArgs
}

// buildEngineContainerConfigs constructs two runtime_env.container configs for
// engine version isolation on SSH clusters (version > v1.0.0):
//
//   - baseConfig:    engine image + --rm only. Used as the application-level
//     runtime_env.container so the app_builder and Controller can run on any
//     node (no GPU required).
//   - backendConfig: engine image + --rm + GPU options + volume mounts + NFS.
//     Set on Backend deployment's ray_actor_options.runtime_env.container to
//     override the app-level config. Ray replaces "container" per-key (no deep
//     merge), so this must be self-contained.
func buildEngineContainerConfigs(endpoint *v1.Endpoint,
	engine *v1.Engine, imageRegistry *v1.ImageRegistry,
	acceleratorMgr accelerator.Manager,
	modelCaches []v1.ModelCache, modelRegistry *v1.ModelRegistry) (baseConfig, backendConfig map[string]interface{}, err error) {
	if endpoint == nil || endpoint.Spec == nil || endpoint.Spec.Engine == nil {
		return nil, nil, errors.New("endpoint with engine spec is required for SSH cluster")
	}

	if engine == nil || engine.Spec == nil {
		return nil, nil, errors.New("engine is required for SSH cluster")
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
		return nil, nil, errors.Errorf("engine version %s not found in engine %s", endpoint.Spec.Engine.Version, engine.Metadata.Name)
	}

	// Get accelerator type from endpoint resources (consistent with K8s orchestrator).
	// Default to "cpu" when no accelerator type is specified.
	acceleratorType := ""

	if endpoint.Spec.Resources != nil {
		acceleratorType = endpoint.Spec.Resources.GetAcceleratorType()
	}

	if acceleratorType == "" {
		acceleratorType = acceleratorTypeCPU
	}

	// Look up engine image and build full image reference
	imagePrefix := ""
	if imageRegistry != nil {
		imagePrefix, err = util.GetImagePrefix(imageRegistry)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "failed to get image prefix from registry")
		}
	}

	// SSH clusters use GetImageForSSHAccelerator which tries "ssh_<type>" first,
	// then falls back to generic accelerator key.
	engineImage := targetVersion.GetImageForSSHAccelerator(acceleratorType)
	if engineImage == nil {
		return nil, nil, errors.Errorf("no engine image configured for accelerator %q in engine %s version %s",
			acceleratorType, engine.Metadata.Name, endpoint.Spec.Engine.Version)
	}

	imageRef := util.BuildEngineImageRef(imagePrefix, engineImage)

	// Base config: engine image + --rm only (for app_builder and Controller).
	baseConfig = map[string]interface{}{
		"image":       imageRef,
		"run_options": []string{"--rm"},
	}

	// Backend config: starts with the same base, then adds GPU options, volumes, and NFS.
	var backendRunOptions []string

	// Get accelerator-specific run_options from plugin (skip for CPU — no special runtime needed)
	if acceleratorMgr != nil && acceleratorType != "" && acceleratorType != acceleratorTypeCPU {
		opts, err := acceleratorMgr.GetEngineContainerRunOptions(acceleratorType)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "failed to get engine container run options for accelerator %s", acceleratorType)
		}

		backendRunOptions = append(backendRunOptions, opts...)
	}

	// Mount model caches using HOST paths (docker.sock creates containers on host)
	for _, mc := range modelCaches {
		if mc.HostPath != nil {
			containerMountPath := filepath.Join(v1.DefaultSSHClusterModelCacheMountPath, mc.Name)
			backendRunOptions = append(backendRunOptions, fmt.Sprintf("-v %s:%s", mc.HostPath.Path, containerMountPath))
		}
	}

	// Mount NFS model registry directly into the engine container via Docker NFS volume.
	if modelRegistry != nil && modelRegistry.Spec != nil && modelRegistry.Spec.Type == v1.BentoMLModelRegistryType {
		registryURL, err := url.Parse(modelRegistry.Spec.Url)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "failed to parse model registry URL %s", modelRegistry.Spec.Url)
		}

		if registryURL.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			registry, err := model_registry.NewModelRegistry(modelRegistry)
			if err != nil {
				return nil, nil, errors.Wrap(err, "failed to create model registry for NFS type detection")
			}

			nfsVersion, err := registry.GetNFSVersion()
			if err != nil {
				return nil, nil, errors.Wrap(err, "failed to detect NFS version from control-plane mount")
			}

			if nfsVersion == "" {
				return nil, nil, errors.New("NFS mount not found on control-plane, cannot determine NFS version for engine container")
			}

			nfsMountPath := filepath.Join("/mnt", endpoint.Metadata.Workspace, endpoint.Metadata.Name)
			// Always use type=nfs (nfs4 filesystem type removed in kernel 5.6+).
			// Add explicit nfsvers with the precise version detected from the
			// control-plane mount, since Docker calls mount(2) directly and
			// older kernels won't auto-negotiate.
			if nfsVersion != "3" {
				backendRunOptions = append(backendRunOptions, fmt.Sprintf(
					`--mount 'type=volume,dst=%s,volume-opt=type=nfs,"volume-opt=o=addr=%s,nfsvers=%s",volume-opt=device=:%s'`,
					nfsMountPath, registryURL.Hostname(), nfsVersion, registryURL.Path))
			} else {
				backendRunOptions = append(backendRunOptions, fmt.Sprintf(
					"--mount 'type=volume,dst=%s,volume-opt=type=nfs,volume-opt=o=addr=%s,volume-opt=device=:%s'",
					nfsMountPath, registryURL.Hostname(), registryURL.Path))
			}
		}
	}

	// Auto-remove engine container when it exits to prevent residual containers on the host.
	backendRunOptions = append(backendRunOptions, "--rm")

	backendConfig = map[string]interface{}{
		"image":       imageRef,
		"run_options": backendRunOptions,
	}

	return baseConfig, backendConfig, nil
}

func setEngineSpecialEnv(endpoint *v1.Endpoint, deployedCluster *v1.Cluster, applicationEnv map[string]string) {
	// Old clusters (<= v1.0.0) use RAY_kill_child_processes_on_worker_exit_with_raylet_subreaper which causes
	// parent processes to lose child exit codes, breaking vLLM's P2P check. For those clusters, skip the check.
	// New clusters (> v1.0.0) use RAY_process_group_cleanup_enabled which doesn't have this issue.
	if endpoint.Spec != nil && endpoint.Spec.Engine != nil && endpoint.Spec.Engine.Engine == v1.EngineNameVLLM {
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
