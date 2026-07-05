package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/deploy"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	annEndpointSpecHash              = "neutree.ai/endpoint-spec-hash"
	annNeutreeVersion                = "neutree.ai/neutree-version"
	containerFailureRestartThreshold = 5
	modelDownloaderInitContainerName = "model-downloader"
)

// Kubernetes does not expose these kubelet container reasons as corev1 constants.
// Keep the standard reason strings centralized so status checks and tests share one definition.
const (
	k8sContainerReasonOOMKilled        = "OOMKilled"
	k8sContainerReasonCrashLoopBackOff = "CrashLoopBackOff"
	k8sContainerReasonImagePullBackOff = "ImagePullBackOff"
	k8sContainerReasonErrImagePull     = "ErrImagePull"
	k8sContainerReasonPodInitializing  = "PodInitializing"
)

var _ Orchestrator = &kubernetesOrchestrator{}

type kubernetesOrchestrator struct {
	storage storage.Storage

	acceleratorMgr accelerator.Manager
}

func newKubernetesOrchestrator(opts Options) *kubernetesOrchestrator {
	return &kubernetesOrchestrator{
		storage:        opts.Storage,
		acceleratorMgr: opts.AcceleratorMgr,
	}
}

func (k *kubernetesOrchestrator) prepareOrchestratorContext(endpoint *v1.Endpoint) (*OrchestratorContext, error) {
	deployedCluster, err := getEndpointDeployCluster(k.storage, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get deploy cluster")
	}

	imageRegistry, err := getUsedImageRegistries(deployedCluster, k.storage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get used image registry")
	}

	engine, err := getUsedEngine(k.storage, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get engine")
	}

	modelRegistry, err := getEndpointModelRegistry(k.storage, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get model registry")
	}

	ctrlClient, err := util.GetClientFromCluster(deployedCluster)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get kubernetes client")
	}

	return &OrchestratorContext{
		Cluster:       deployedCluster,
		Engine:        engine,
		ModelRegistry: modelRegistry,
		ImageRegistry: imageRegistry,
		Endpoint:      endpoint,
		ctrClient:     ctrlClient,
		logger:        klog.LoggerWithValues(klog.Background(), "endpoint", endpoint.Metadata.WorkspaceName()),
	}, nil
}

func (k *kubernetesOrchestrator) validateDependencies(ctx *OrchestratorContext) error {
	// validate cluster status
	if ctx.Cluster.Status == nil || ctx.Cluster.Status.Phase != v1.ClusterPhaseRunning {
		return errors.Errorf("deploy cluster %s is not running", ctx.Cluster.Metadata.WorkspaceName())
	}

	if ctx.Cluster.Spec.Type != v1.KubernetesClusterType {
		return errors.Errorf("deploy cluster %s is not kubernetes type", ctx.Cluster.Metadata.WorkspaceName())
	}

	if err := validateAcceleratorVirtualizationDependencies(ctx); err != nil {
		return err
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

func validateAcceleratorVirtualizationDependencies(ctx *OrchestratorContext) error {
	if ctx.Endpoint == nil ||
		ctx.Endpoint.Spec == nil ||
		ctx.Endpoint.Spec.Resources == nil ||
		!ctx.Endpoint.Spec.Resources.HasAcceleratorVirtualization() {
		return nil
	}

	if ctx.Cluster.Spec == nil || !ctx.Cluster.Spec.AcceleratorVirtualizationEnabled() {
		return errors.Errorf(
			"endpoint %s requests accelerator virtualization, but deploy cluster %s accelerator virtualization is not enabled",
			ctx.Endpoint.Metadata.WorkspaceName(),
			ctx.Cluster.Metadata.WorkspaceName(),
		)
	}

	acceleratorVirtualizationStatus := acceleratorVirtualizationComponentStatus(ctx.Cluster)
	if acceleratorVirtualizationStatus == nil || acceleratorVirtualizationStatus.Phase != v1.ComponentPhaseReady {
		statusDetails := ""
		if acceleratorVirtualizationStatus != nil {
			statusDetails = fmt.Sprintf(": %s %s", acceleratorVirtualizationStatus.Reason, acceleratorVirtualizationStatus.Message)
		}

		return errors.Errorf(
			"endpoint %s requests accelerator virtualization, but deploy cluster %s accelerator virtualization component is not ready%s",
			ctx.Endpoint.Metadata.WorkspaceName(),
			ctx.Cluster.Metadata.WorkspaceName(),
			statusDetails,
		)
	}

	return nil
}

func acceleratorVirtualizationComponentStatus(cluster *v1.Cluster) *v1.ComponentStatus {
	if cluster == nil || cluster.Status == nil || cluster.Status.ComponentStatus == nil {
		return nil
	}

	return cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey]
}

func (k *kubernetesOrchestrator) CreateEndpoint(endpoint *v1.Endpoint) error {
	ctx, err := k.prepareOrchestratorContext(endpoint)
	if err != nil {
		return errors.Wrapf(err, "failed to prepare orchestrator context for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	if err := k.validateDependencies(ctx); err != nil {
		return errors.Wrapf(err, "failed to validate dependencies for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	ctx.logger.V(4).Info("Creating or updating endpoint")

	err = k.createEndpoint(ctx)
	if err != nil {
		return errors.Wrapf(err, "failed to create endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	return nil
}

// computeEndpointSpecHash computes a SHA256 hash of the endpoint spec.
func computeEndpointSpecHash(endpoint *v1.Endpoint) (string, error) {
	specJSON, err := json.Marshal(endpoint.Spec)
	if err != nil {
		return "", errors.Wrap(err, "failed to marshal endpoint spec for hashing")
	}

	hash := sha256.Sum256(specJSON)

	return fmt.Sprintf("%x", hash), nil
}

func (k *kubernetesOrchestrator) createEndpoint(ctx *OrchestratorContext) error {
	namespace := util.ClusterNamespace(ctx.Cluster)

	renderVars, err := k.buildManifestVariables(ctx.Endpoint, ctx.Cluster, ctx.ModelRegistry, ctx.Engine, ctx.ImageRegistry)
	if err != nil {
		return errors.Wrapf(err, "failed to build manifest variables for endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
	}

	currentSpecHash, err := computeEndpointSpecHash(ctx.Endpoint)
	if err != nil {
		return errors.Wrapf(err, "failed to compute endpoint spec hash for endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
	}

	// Preserve NeutreeVersion when only the cluster version changed (not the endpoint spec).
	// The spec hash and NeutreeVersion are stored as annotations on the K8s Deployment.
	existingDep := &appsv1.Deployment{}

	if err := ctx.ctrClient.Get(context.Background(), client.ObjectKey{
		Namespace: namespace,
		Name:      ctx.Endpoint.Metadata.Name,
	}, existingDep); err != nil {
		if !apierrors.IsNotFound(err) {
			return errors.Wrapf(err, "failed to get existing deployment for endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
		}
		// Deployment does not exist yet (first deploy) — nothing to preserve.
	} else if existingDep.Annotations != nil {
		storedHash := existingDep.Annotations[annEndpointSpecHash]
		storedVersion := existingDep.Annotations[annNeutreeVersion]

		// Preserve NeutreeVersion when:
		// - storedHash == currentSpecHash: endpoint spec unchanged (cluster-only upgrade)
		// - storedHash == "": legacy endpoint bootstrapped with version but no hash yet
		if storedVersion != "" && (storedHash == "" || storedHash == currentSpecHash) {
			renderVars.NeutreeVersion = storedVersion
		}
	}

	deployTemplate, err := k.getDeployTemplate(ctx.Endpoint, ctx.Engine)
	if err != nil {
		return errors.Wrapf(err, "failed to get deploy template for endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
	}

	deploymentObjects, err := buildDeploymentObjects(deployTemplate, renderVars)
	if err != nil {
		return errors.Wrapf(err, "failed to build deployment for endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
	}

	applier := deploy.NewKubernetesDeployer(
		ctx.ctrClient,
		namespace,
		ctx.Endpoint.Metadata.Name, // resourceName
		"deployment",               // componentName
	).
		WithNewObjects(deploymentObjects).
		WithLabels(map[string]string{
			"endpoint":                         ctx.Endpoint.Metadata.Name,
			v1.NeutreeClusterLabelKey:          ctx.Cluster.Metadata.Name,
			v1.NeutreeClusterWorkspaceLabelKey: ctx.Cluster.Metadata.Workspace,
			v1.LabelManagedBy:                  v1.LabelManagedByValue,
		}).
		WithMutate(func(obj *unstructured.Unstructured) error {
			// Inject spec hash and NeutreeVersion as annotations on the Deployment
			// so they are included in the SSA apply and managed by the field owner.
			if obj.GetKind() == "Deployment" {
				ann := obj.GetAnnotations()
				if ann == nil {
					ann = make(map[string]string)
				}

				ann[annEndpointSpecHash] = currentSpecHash
				ann[annNeutreeVersion] = renderVars.NeutreeVersion
				obj.SetAnnotations(ann)
			}

			return nil
		}).
		WithLogger(ctx.logger)

	// Apply manifests (automatically handles configuration storage)
	changedCount, err := applier.Apply(context.Background())
	if err != nil {
		return errors.Wrap(err, "failed to apply endpoint")
	}

	if changedCount > 0 {
		ctx.logger.Info("Applied endpoint manifests",
			"changedObjects", changedCount)
	}

	// Bootstrap: patch annotations on Deployments that don't have them yet.
	// WithMutate only runs on changed objects (spec diff > 0). For no-op reconciles
	// (e.g., existing endpoints deployed before this code, or stable endpoints after
	// a cluster upgrade where NeutreeVersion was preserved), this patch is the only
	// path that writes/refreshes annotations. Annotation-only changes do not trigger
	// a rollout.
	dep := &appsv1.Deployment{}
	if err := ctx.ctrClient.Get(context.Background(), client.ObjectKey{
		Namespace: namespace,
		Name:      ctx.Endpoint.Metadata.Name,
	}, dep); err == nil {
		if dep.Annotations == nil || dep.Annotations[annNeutreeVersion] == "" || dep.Annotations[annEndpointSpecHash] == "" {
			patch := client.MergeFrom(dep.DeepCopy())

			if dep.Annotations == nil {
				dep.Annotations = make(map[string]string)
			}

			dep.Annotations[annEndpointSpecHash] = currentSpecHash
			dep.Annotations[annNeutreeVersion] = renderVars.NeutreeVersion

			if patchErr := ctx.ctrClient.Patch(context.Background(), dep, patch); patchErr != nil {
				ctx.logger.Error(patchErr, "Failed to bootstrap deployment annotations")
			}
		}
	}

	return nil
}

// PauseEndpoint scales the endpoint's K8s deployment to zero replicas.
//
// Pause does not need ModelRegistry/Engine/ImageRegistry — the existing K8s
// deployment already has the rendered manifest, so this just GETs it and
// merge-patches spec.replicas to 0. A paused endpoint therefore converges to
// Paused even when the model has been removed.
//
// TODO: like getEndpointStats, this currently assumes a single Deployment per
// endpoint. Multi-kind deploy modes (multiple Deployments, LeaderWorkerSet)
// will need pause + status reporting extended together.
func (k *kubernetesOrchestrator) PauseEndpoint(endpoint *v1.Endpoint) error {
	ctx, err := k.prepareOrchestratorContextForPauseDelete(endpoint)
	if err != nil {
		return errors.Wrapf(err, "failed to prepare orchestrator context for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	if err := k.validateClusterForPauseDelete(ctx); err != nil {
		return errors.Wrapf(err, "failed to validate cluster for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	ctx.logger.V(4).Info("Pausing endpoint by patching replicas to 0")

	if err := k.pauseEndpoint(ctx); err != nil {
		return errors.Wrapf(err, "failed to pause endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	return nil
}

// DeleteEndpoint deletes the endpoint's K8s resources via the deployer's
// last-applied snapshot (stored in a ConfigMap by Apply).
//
// Delete does not need ModelRegistry/Engine/ImageRegistry: configStore.Get
// returns the last-applied manifest list directly, so deletion proceeds even
// when those dependencies have been removed.
//
// Intentionally does NOT call validateClusterForPauseDelete — delete must remain
// permissive on degraded clusters; the fallback is the force-delete
// annotation handled in the controller.
func (k *kubernetesOrchestrator) DeleteEndpoint(endpoint *v1.Endpoint) error {
	ctx, err := k.prepareOrchestratorContextForPauseDelete(endpoint)
	if err != nil {
		return errors.Wrapf(err, "failed to prepare orchestrator context for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	ctx.logger.V(4).Info("Deleting endpoint")

	if err := k.deleteEndpoint(ctx); err != nil {
		return errors.Wrapf(err, "failed to delete endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	return nil
}

// prepareOrchestratorContextForPauseDelete is the pause/delete equivalent of
// prepareOrchestratorContext: it fetches only what those operations actually
// need (cluster + ctrlClient) and skips ModelRegistry/Engine/ImageRegistry
// lookups so a removed model registry does not block convergence to
// Paused/Deleted.
func (k *kubernetesOrchestrator) prepareOrchestratorContextForPauseDelete(endpoint *v1.Endpoint) (*OrchestratorContext, error) {
	deployedCluster, err := getEndpointDeployCluster(k.storage, endpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get deploy cluster")
	}

	ctrlClient, err := util.GetClientFromCluster(deployedCluster)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get kubernetes client")
	}

	return &OrchestratorContext{
		Cluster:   deployedCluster,
		Endpoint:  endpoint,
		ctrClient: ctrlClient,
		logger:    klog.LoggerWithValues(klog.Background(), "endpoint", endpoint.Metadata.WorkspaceName()),
	}, nil
}

// validateClusterForPauseDelete enforces the subset of validateDependencies that
// pause/delete still need: cluster must exist, be Running, and be a K8s
// cluster. Other dependency checks (engine, model registry, image registry)
// are intentionally skipped — they are not required to scale or delete the
// existing K8s objects.
func (k *kubernetesOrchestrator) validateClusterForPauseDelete(ctx *OrchestratorContext) error {
	if ctx.Cluster.Status == nil || ctx.Cluster.Status.Phase != v1.ClusterPhaseRunning {
		return errors.Errorf("deploy cluster %s is not running", ctx.Cluster.Metadata.WorkspaceName())
	}

	if ctx.Cluster.Spec.Type != v1.KubernetesClusterType {
		return errors.Errorf("deploy cluster %s is not kubernetes type", ctx.Cluster.Metadata.WorkspaceName())
	}

	return nil
}

// pauseEndpoint patches the endpoint's existing Deployment to spec.replicas=0.
// Idempotent: returns nil when the deployment is already at 0 replicas or
// when the deployment does not exist (already paused / never deployed).
func (k *kubernetesOrchestrator) pauseEndpoint(ctx *OrchestratorContext) error {
	namespace := util.ClusterNamespace(ctx.Cluster)

	dep := &appsv1.Deployment{}
	if err := ctx.ctrClient.Get(context.Background(), client.ObjectKey{
		Namespace: namespace,
		Name:      ctx.Endpoint.Metadata.Name,
	}, dep); err != nil {
		if apierrors.IsNotFound(err) {
			ctx.logger.V(4).Info("Deployment not found, treating pause as no-op")
			return nil
		}

		return errors.Wrapf(err, "failed to get deployment for endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
	}

	if dep.Spec.Replicas != nil && *dep.Spec.Replicas == 0 {
		ctx.logger.V(4).Info("Deployment already at replicas=0, treating pause as no-op")
		return nil
	}

	patch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"replicas":0}}`))
	if err := ctx.ctrClient.Patch(context.Background(), dep, patch); err != nil {
		return errors.Wrapf(err, "failed to patch endpoint %s replicas to 0", ctx.Endpoint.Metadata.WorkspaceName())
	}

	return nil
}

func (k *kubernetesOrchestrator) deleteEndpoint(ctx *OrchestratorContext) error {
	applier := deploy.NewKubernetesDeployer(
		ctx.ctrClient,
		util.ClusterNamespace(ctx.Cluster),
		ctx.Endpoint.Metadata.Name,
		"deployment",
	).WithLogger(ctx.logger)

	// Delete all resources
	deleteFinished, err := applier.Delete(context.Background())
	if err != nil {
		return errors.Wrapf(err, "failed to delete endpoint %s resources", ctx.Endpoint.Metadata.WorkspaceName())
	}

	if !deleteFinished {
		ctx.logger.Info("waiting for endpoint to be fully deleted")
	}

	return nil
}

func (k *kubernetesOrchestrator) GetEndpointStatus(endpoint *v1.Endpoint) (*v1.EndpointStatus, error) {
	deployedCluster, err := getEndpointDeployCluster(k.storage, endpoint)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get deploy cluster for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	if deployedCluster.Spec.Type != v1.KubernetesClusterType {
		return nil, errors.Errorf("endpoint %s deploy cluster %s is not kubernetes type", endpoint.Metadata.Name, deployedCluster.Metadata.Name)
	}

	ctrlClient, err := util.GetClientFromCluster(deployedCluster)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get kubernetes client for cluster %s", deployedCluster.Metadata.Name)
	}

	return k.getEndpointStats(ctrlClient, util.ClusterNamespace(deployedCluster), deployedCluster, endpoint)
}

func (k *kubernetesOrchestrator) getEndpointStats(
	ctrlClient client.Client,
	namespace string,
	cluster *v1.Cluster,
	endpoint *v1.Endpoint,
) (*v1.EndpointStatus, error) {
	var exists bool

	// now we only use deployment to deploy endpoint,
	// but in the future, we may support more deploy mode, like distribute inference or pd inference,
	// and then, we may expand the status checking logic.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      endpoint.Metadata.Name,
			Namespace: namespace,
		},
	}

	err := ctrlClient.Get(context.Background(), client.ObjectKeyFromObject(dep), dep)
	if err != nil {
		if apierrors.IsNotFound(err) {
			exists = false
		} else {
			return nil, errors.Wrapf(err, "failed to get deployment for endpoint %s", endpoint.Metadata.WorkspaceName())
		}
	} else {
		exists = true
	}

	isDeleting := endpoint.GetDeletionTimestamp() != ""

	if isDeleting {
		// Check pods directly instead of Deployment for deletion status.
		// Deployment is deleted immediately (K8s Background propagation),
		// but pods linger while GC terminates them. This ensures the status
		// stays DELETING long enough for the deployer's ConfigMap cleanup cycle.
		pods, err := k.listPods(ctrlClient, namespace, map[string]string{
			"app":      "inference",
			"endpoint": endpoint.Metadata.Name,
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list pods for endpoint %s", endpoint.Metadata.WorkspaceName())
		}

		if len(pods) > 0 {
			return &v1.EndpointStatus{
				Phase:        v1.EndpointPhaseDELETING,
				ErrorMessage: fmt.Sprintf("Endpoint deleting in progress: waiting for %d pod(s) to terminate", len(pods)),
			}, nil
		}

		return &v1.EndpointStatus{
			Phase: v1.EndpointPhaseDELETED,
		}, nil
	}

	// Paused check goes BEFORE !exists: a paused-from-start endpoint
	// (replicas=0 on first apply) has no deployment by design — that is
	// already the Paused steady state, not "deployment missing". Mirrors
	// Ray.GetEndpointStatus which orders these checks the same way.
	isPaused := IsEndpointPaused(endpoint)
	if isPaused && !exists {
		return &v1.EndpointStatus{
			Phase: v1.EndpointPhasePAUSED,
		}, nil
	}

	if !exists {
		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseDEPLOYING,
			ErrorMessage: "Endpoint deploying in progress: Endpoint deployment not found in namespace " + namespace,
		}, nil
	}

	// Check for CrashLoopBackOff or other critical failures
	pods, err := k.listPods(ctrlClient, namespace, dep.Spec.Selector.MatchLabels)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get pods for deployment %s", dep.Name)
	}

	if isPaused {
		if len(pods) == 0 {
			return &v1.EndpointStatus{
				Phase: v1.EndpointPhasePAUSED,
			}, nil
		} else {
			return &v1.EndpointStatus{
				Phase:        v1.EndpointPhaseDEPLOYING,
				ErrorMessage: "Endpoint pausing in progress: waiting for all pods to terminate",
			}, nil
		}
	}

	resources, err := k.buildEndpointResourceStatus(ctrlClient, cluster, endpoint)
	if err != nil {
		klog.Warningf("failed to build resource status for endpoint %s: %v", endpoint.Metadata.WorkspaceName(), err)
	}

	// Check if all pods are ready and updated
	if util.IsDeploymentUpdatedAndReady(dep) {
		return &v1.EndpointStatus{
			Phase:     v1.EndpointPhaseRUNNING,
			Resources: resources,
		}, nil
	}

	if hasFailed, failedMsg := k.checkPodFailures(pods); hasFailed {
		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseFAILED,
			ErrorMessage: "Endpoint failed: " + failedMsg,
			Resources:    resources,
		}, nil
	}

	if hasIncomplete, detail := hasIncompleteModelDownloaderInitContainer(pods); hasIncomplete {
		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseMODELDOWNLOADING,
			ErrorMessage: "Endpoint model download in progress: " + detail,
		}, nil
	}

	// Otherwise, still deploying
	errorMessage := k.buildDeploymentErrorMessage(dep)

	return &v1.EndpointStatus{
		Phase:        v1.EndpointPhaseDEPLOYING,
		ErrorMessage: "Endpoint deploying in progress: " + errorMessage,
		Resources:    resources,
	}, nil
}

func (k *kubernetesOrchestrator) buildEndpointResourceStatus(
	ctrlClient client.Client,
	cluster *v1.Cluster,
	endpoint *v1.Endpoint,
) (*v1.EndpointResourceStatus, error) {
	if k.acceleratorMgr == nil {
		return nil, nil
	}

	parsers := k.acceleratorMgr.GetAllParsers()
	resourceClient := resourceview.NewK8sResourceClient(ctrlClient, parsers)
	resourceBuilder := resourceview.NewResourceViewBuilder(resourceClient)

	return resourceBuilder.BuildEndpointResources(context.Background(), cluster, endpoint)
}

// listPods lists pods matching the given labels in the specified namespace.
func (k *kubernetesOrchestrator) listPods(ctrlClient client.Client, namespace string, labels map[string]string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}

	err := ctrlClient.List(context.Background(), podList,
		client.InNamespace(namespace),
		client.MatchingLabels(labels),
	)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list pods in namespace %s", namespace)
	}

	return podList.Items, nil
}

func hasIncompleteModelDownloaderInitContainer(pods []corev1.Pod) (bool, string) {
	for _, pod := range pods {
		for _, initStatus := range pod.Status.InitContainerStatuses {
			if initStatus.Name != modelDownloaderInitContainerName {
				continue
			}

			if initStatus.State.Terminated != nil && initStatus.State.Terminated.ExitCode == 0 {
				continue
			}

			if initStatus.State.Running != nil {
				return true, modelDownloaderInitContainerName + " init container is running"
			}

			if initStatus.State.Waiting != nil {
				message := fmt.Sprintf("%s init container is waiting (reason=%s)",
					modelDownloaderInitContainerName, initStatus.State.Waiting.Reason)
				if initStatus.State.Waiting.Message != "" {
					message += ": " + initStatus.State.Waiting.Message
				}

				return true, message
			}

			if initStatus.State.Terminated != nil {
				message := fmt.Sprintf("%s init container terminated with exit code %d after %d restart(s), retrying model download",
					modelDownloaderInitContainerName, initStatus.State.Terminated.ExitCode, initStatus.RestartCount)
				if initStatus.State.Terminated.Message != "" {
					message += ": " + initStatus.State.Terminated.Message
				}

				return true, message
			}

			return true, modelDownloaderInitContainerName + " init container has not started; waiting for model download to start"
		}
	}

	return false, ""
}

// checkContainerStatuses checks a slice of container statuses for critical failures.
// containerType should be "Container" or "Init Container" for error message clarity.
func checkContainerStatuses(podName string, statuses []corev1.ContainerStatus, containerType string) (bool, []string) {
	var (
		failed   bool
		errorMsg []string
	)

	for _, cs := range statuses {
		// Check for OOMKilled in both current state and last termination state.
		// The first OOM kill appears in State.Terminated before any restart;
		// subsequent OOM kills appear in LastTerminationState.Terminated.
		if (cs.State.Terminated != nil && cs.State.Terminated.Reason == k8sContainerReasonOOMKilled) ||
			(cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason == k8sContainerReasonOOMKilled) {
			failed = true

			errorMsg = append(errorMsg, fmt.Sprintf("Pod '%s' %s '%s' was killed due to OOM (Out of Memory)",
				podName, containerType, cs.Name))

			continue
		}

		if containerType == "Init Container" &&
			cs.State.Terminated != nil &&
			cs.State.Terminated.ExitCode != 0 &&
			cs.RestartCount >= containerFailureRestartThreshold {
			failed = true

			errorMsg = append(errorMsg, fmt.Sprintf("Pod '%s' %s '%s' terminated with exit code %d after %d restarts: %s",
				podName, containerType, cs.Name, cs.State.Terminated.ExitCode, cs.RestartCount, cs.State.Terminated.Message))

			continue
		}

		// Check for CrashLoopBackOff with restart count >= 5
		if cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason

			if reason == k8sContainerReasonCrashLoopBackOff && cs.RestartCount >= containerFailureRestartThreshold {
				failed = true

				errorMsg = append(errorMsg, fmt.Sprintf("Pod '%s' %s '%s' in CrashLoopBackOff (restarted %d times): %s",
					podName, containerType, cs.Name, cs.RestartCount, cs.State.Waiting.Message))

				continue
			}
			// Check for ImagePullBackOff
			if reason == k8sContainerReasonImagePullBackOff || reason == k8sContainerReasonErrImagePull {
				failed = true

				errorMsg = append(errorMsg, fmt.Sprintf("Pod '%s' %s '%s' failed to pull image: %s",
					podName, containerType, cs.Name, cs.State.Waiting.Message))

				continue
			}
		}
	}

	return failed, errorMsg
}

// checkPodFailures checks if any pods have critical failures like CrashLoopBackOff
func (k *kubernetesOrchestrator) checkPodFailures(pods []corev1.Pod) (bool, string) {
	failed := false
	var errorMsg []string

	for _, pod := range pods {
		// Check init container statuses
		if f, msgs := checkContainerStatuses(pod.Name, pod.Status.InitContainerStatuses, "Init Container"); f {
			failed = true

			errorMsg = append(errorMsg, msgs...)
		}

		// Check container statuses
		if f, msgs := checkContainerStatuses(pod.Name, pod.Status.ContainerStatuses, "Container"); f {
			failed = true

			errorMsg = append(errorMsg, msgs...)
		}

		// Check for pod scheduling failures
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
				if cond.Reason == "Unschedulable" {
					failed = true

					errorMsg = append(errorMsg, fmt.Sprintf("Pod '%s' is unschedulable: %s", pod.Name, cond.Message))
				}
			}
		}
	}

	return failed, strings.Join(errorMsg, "; ")
}

// buildDeploymentErrorMessage builds a descriptive error message from deployment conditions
func (k *kubernetesOrchestrator) buildDeploymentErrorMessage(dep *appsv1.Deployment) string {
	var errorMessage []string

	for _, condition := range dep.Status.Conditions {
		if condition.Status == corev1.ConditionTrue {
			continue
		}

		errorMessage = append(errorMessage, fmt.Sprintf("Type: %s, Reason: %s, Message: %s",
			condition.Type, condition.Reason, condition.Message))
	}

	if dep.Spec.Replicas != nil {
		errorMessage = append(errorMessage, fmt.Sprintf("Deployment: %d/%d replicas ready",
			dep.Status.ReadyReplicas, *dep.Spec.Replicas))
		errorMessage = append(errorMessage, fmt.Sprintf("Deployment: %d/%d replicas updated",
			dep.Status.UpdatedReplicas, *dep.Spec.Replicas))
		errorMessage = append(errorMessage, fmt.Sprintf("Deployment: %d/%d replicas available",
			dep.Status.AvailableReplicas, *dep.Spec.Replicas))
	}

	if len(errorMessage) == 0 {
		return "Deployment is progressing"
	}

	return strings.Join(errorMessage, "; ")
}
