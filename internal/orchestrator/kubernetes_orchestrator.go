package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/deploy"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

func (k *kubernetesOrchestrator) createEndpoint(ctx *OrchestratorContext) error {
	renderVars, err := k.buildManifestVariables(ctx.Endpoint, ctx.Cluster, ctx.ModelRegistry, ctx.Engine, ctx.ImageRegistry)
	if err != nil {
		return errors.Wrapf(err, "failed to build manifest variables for endpoint %s", ctx.Endpoint.Metadata.WorkspaceName())
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
		util.ClusterNamespace(ctx.Cluster),
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

	return nil
}

func (k *kubernetesOrchestrator) PauseEndpoint(endpoint *v1.Endpoint) error {
	ctx, err := k.prepareOrchestratorContext(endpoint)
	if err != nil {
		return errors.Wrapf(err, "failed to prepare orchestrator context for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	if err := k.validateDependencies(ctx); err != nil {
		return errors.Wrapf(err, "failed to validate dependencies for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	ctx.logger.V(4).Info("Pausing by reapplying with zero replicas")

	err = k.createEndpoint(ctx)
	if err != nil {
		return errors.Wrapf(err, "failed to pause endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	return nil
}

func (k *kubernetesOrchestrator) DeleteEndpoint(endpoint *v1.Endpoint) error {
	// delete endpoint should not validate dependencies
	ctx, err := k.prepareOrchestratorContext(endpoint)
	if err != nil {
		return errors.Wrapf(err, "failed to prepare orchestrator context for endpoint %s", endpoint.Metadata.WorkspaceName())
	}

	ctx.logger.V(4).Info("Deleting endpoint")

	err = k.deleteEndpoint(ctx)
	if err != nil {
		return errors.Wrapf(err, "failed to delete endpoint %s", endpoint.Metadata.WorkspaceName())
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

	return k.getEndpointStats(ctrlClient, util.ClusterNamespace(deployedCluster), endpoint)
}

func (k *kubernetesOrchestrator) getEndpointStats(ctrlClient client.Client, namespace string, endpoint *v1.Endpoint) (*v1.EndpointStatus, error) {
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
		if !exists {
			return &v1.EndpointStatus{
				Phase: v1.EndpointPhaseDELETED,
			}, nil
		}

		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseDELETING,
			ErrorMessage: "Endpoint deleting in progress: waiting for endpoint to be fully deleted",
		}, nil
	}

	if !exists {
		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseDEPLOYING,
			ErrorMessage: "Endpoint deploying in progress: Endpoint deployment not found in namespace " + namespace,
		}, nil
	}

	// Check for CrashLoopBackOff or other critical failures
	pods, err := k.getPodsForDeployment(dep, ctrlClient)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get pods for deployment %s", dep.Name)
	}

	isPaused := IsEndpointPaused(endpoint)
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

	// Check if all pods are ready and updated
	if util.IsDeploymentUpdatedAndReady(dep) {
		return &v1.EndpointStatus{
			Phase: v1.EndpointPhaseRUNNING,
		}, nil
	}

	if hasFailed, failedMsg := k.checkPodFailures(pods); hasFailed {
		return &v1.EndpointStatus{
			Phase:        v1.EndpointPhaseFAILED,
			ErrorMessage: "Endpoint failed: " + failedMsg,
		}, nil
	}

	// Otherwise, still deploying
	errorMessage := k.buildDeploymentErrorMessage(dep)

	return &v1.EndpointStatus{
		Phase:        v1.EndpointPhaseDEPLOYING,
		ErrorMessage: "Endpoint deploying in progress: " + errorMessage,
	}, nil
}

// getPodsForDeployment retrieves pods managed by the given deployment
func (k *kubernetesOrchestrator) getPodsForDeployment(dep *appsv1.Deployment, ctrlClient client.Client) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}

	err := ctrlClient.List(context.Background(), podList,
		client.InNamespace(dep.Namespace),
		client.MatchingLabels(dep.Spec.Selector.MatchLabels),
	)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list pods for deployment %s", dep.Name)
	}

	return podList.Items, nil
}

// checkPodFailures checks if any pods have critical failures like CrashLoopBackOff
func (k *kubernetesOrchestrator) checkPodFailures(pods []corev1.Pod) (bool, string) {
	failed := false
	var errorMsg []string

	for _, pod := range pods {
		// Check container statuses
		for _, cs := range pod.Status.ContainerStatuses {
			// Check for OOMKilled
			if cs.LastTerminationState.Terminated != nil {
				if cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
					failed = true

					errorMsg = append(errorMsg, fmt.Sprintf("Pod '%s' Container '%s' was killed due to OOM (Out of Memory)",
						pod.Name, cs.Name))

					continue
				}
			}

			// Check for CrashLoopBackOff with restart count >= 5
			if cs.State.Waiting != nil {
				reason := cs.State.Waiting.Reason

				if reason == "CrashLoopBackOff" && cs.RestartCount >= 5 {
					failed = true

					errorMsg = append(errorMsg, fmt.Sprintf("Pod '%s' Container '%s' in CrashLoopBackOff (restarted %d times): %s",
						pod.Name, cs.Name, cs.RestartCount, cs.State.Waiting.Message))

					continue
				}
				// Check for ImagePullBackOff
				if reason == "ImagePullBackOff" || reason == "ErrImagePull" {
					failed = true

					errorMsg = append(errorMsg, fmt.Sprintf("Pod '%s' Container '%s' failed to pull image: %s",
						pod.Name, cs.Name, cs.State.Waiting.Message))

					continue
				}
			}
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
