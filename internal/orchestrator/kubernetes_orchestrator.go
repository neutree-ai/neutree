package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/manifestapply"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	modelCacheMountPathPrefix = "/root/.neutree/model-cache/"
)

var _ Orchestrator = &kubernetesOrchestrator{}

type kubernetesOrchestrator struct {
	storage storage.Storage

	acceleratorManager accelerator.Manager
}

func newKubernetesOrchestrator(opts Options) *kubernetesOrchestrator {
	return &kubernetesOrchestrator{
		storage:            opts.Storage,
		acceleratorManager: opts.AcceleratorManager,
	}
}

func (k *kubernetesOrchestrator) CreateEndpoint(endpoint *v1.Endpoint) (*v1.EndpointStatus, error) {
	deployedCluster, err := getEndpointDeployCluster(k.storage, endpoint)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get deploy cluster for endpoint %s", endpoint.Metadata.Name)
	}

	if deployedCluster.Spec.Type != v1.KubernetesClusterType {
		return nil, errors.Errorf("endpoint %s deploy cluster %s is not kubernetes type", endpoint.Metadata.Name, deployedCluster.Metadata.Name)
	}

	imageRegistry, err := getUsedImageRegistries(deployedCluster, k.storage)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get used image registry for cluster %s", deployedCluster.Metadata.Name)
	}

	engine, err := getUsedEngine(k.storage, endpoint)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get engine for endpoint %s", endpoint.Metadata.Name)
	}

	modelRegistry, err := getEndpointModelRegistry(k.storage, endpoint)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get model registry for endpoint %s", endpoint.Metadata.Name)
	}

	renderVars, err := k.buildManifestVariables(endpoint, deployedCluster, modelRegistry, engine, imageRegistry)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to build manifest variables for endpoint %s", endpoint.Metadata.Name)
	}

	deployTemplate, err := k.getDeployTemplate(endpoint, engine)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get deploy template for endpoint %s", endpoint.Metadata.Name)
	}

	deploymentObjects, err := buildDeploymentObjects(deployTemplate, renderVars)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to build deployment for endpoint %s", endpoint.Metadata.Name)
	}

	ctrlClient, err := util.GetClientFromCluster(deployedCluster)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get kubernetes client for cluster %s", deployedCluster.Metadata.Name)
	}

	// Get last applied config from endpoint annotations
	lastAppliedConfig := ""
	if endpoint.Metadata.Annotations != nil {
		lastAppliedConfig = endpoint.Metadata.Annotations[v1.AnnotationLastAppliedConfig]
	}

	// Create mutate callback to set ownership and labels
	mutate := func(obj *unstructured.Unstructured) error {
		// Set unified labels for resource tracking
		labels := obj.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}

		labels[v1.LabelManagedBy] = v1.LabelManagedByValue
		obj.SetLabels(labels)

		return nil
	}

	logger := klog.LoggerWithValues(klog.Background(),
		"endpoint", endpoint.Metadata.WorkspaceName(),
	)

	// Use ManifestApply for automatic manifest management
	manifestApply := manifestapply.NewManifestApply(ctrlClient, util.ClusterNamespace(deployedCluster)).
		WithLastAppliedConfig(lastAppliedConfig).
		WithNewObjects(deploymentObjects).
		WithMutate(mutate).
		WithLogger(logger)

	// Apply manifests (automatically handles diff computation, apply, and deletion)
	changedCount, err := manifestApply.ApplyManifests(context.Background())
	if err != nil {
		return nil, errors.Wrap(err, "failed to apply manifests")
	}

	if changedCount > 0 {
		klog.InfoS("Applied endpoint manifests",
			"endpoint", endpoint.Metadata.Name,
			"workspace", endpoint.Metadata.Workspace,
			"changedObjects", changedCount)

		// Save the current configuration as last applied config
		newConfigJSON, err := json.Marshal(deploymentObjects.Items)
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal deployment objects")
		}

		// Initialize annotations if needed
		if endpoint.Metadata.Annotations == nil {
			endpoint.Metadata.Annotations = make(map[string]string)
		}

		// Update last applied config in annotations
		endpoint.Metadata.Annotations[v1.AnnotationLastAppliedConfig] = string(newConfigJSON)
		// Save cluster annotations (including last applied configs from components)
		err = k.storage.UpdateEndpoint(endpoint.GetID(), &v1.Endpoint{
			Metadata: endpoint.Metadata,
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to update cluster annotations")
		}

		klog.InfoS("Updated endpoint configuration",
			"endpoint", endpoint.Metadata.Name)
	}

	return k.GetEndpointStatus(endpoint)
}

func (k *kubernetesOrchestrator) DeleteEndpoint(endpoint *v1.Endpoint) error {
	deployedCluster, err := getEndpointDeployCluster(k.storage, endpoint)
	if err != nil {
		if err != storage.ErrResourceNotFound {
			return errors.Wrapf(err, "failed to get deploy cluster for endpoint %s", endpoint.Metadata.WorkspaceName())
		}
		// If the deployed cluster is not found, we assume the endpoint does not exist.
		return nil
	}

	if deployedCluster.Spec.Type != v1.KubernetesClusterType {
		return errors.Errorf("endpoint %s deploy cluster %s is not kubernetes type", endpoint.Metadata.Name, deployedCluster.Metadata.WorkspaceName())
	}

	ctrlClient, err := util.GetClientFromCluster(deployedCluster)
	if err != nil {
		return errors.Wrapf(err, "failed to get kubernetes client for cluster %s", deployedCluster.Metadata.WorkspaceName())
	}

	// Get last applied config from endpoint annotations
	lastAppliedConfig := ""
	if endpoint.Metadata.Annotations != nil {
		lastAppliedConfig = endpoint.Metadata.Annotations[v1.AnnotationLastAppliedConfig]
	}

	// Use ManifestApply for automatic resource deletion
	manifestApply := manifestapply.NewManifestApply(ctrlClient, util.ClusterNamespace(deployedCluster)).
		WithLastAppliedConfig(lastAppliedConfig)

	// Delete all resources from last applied config
	deleteFinished, err := manifestApply.Delete(context.Background())
	if err != nil {
		return errors.Wrap(err, "failed to delete endpoint resources")
	}

	if !deleteFinished {
		return fmt.Errorf("waiting for endpoint %s to be fully deleted", endpoint.Metadata.Name)
	}

	klog.InfoS("Successfully deleted all endpoint resources",
		"endpoint", endpoint.Metadata.Name,
		"workspace", endpoint.Metadata.Workspace)

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

	// now we only use deployment to deploy endpoint,
	// but in the future, we may support more deploy mode, like distribute inference or pd inference,
	// and then, we may expand the status checking logic.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      endpoint.Metadata.Name,
			Namespace: util.ClusterNamespace(deployedCluster),
		},
	}

	err = ctrlClient.Get(context.Background(), client.ObjectKeyFromObject(dep), dep)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get deployment for endpoint %s", endpoint.Metadata.Name)
	}

	// todo: set endpoint status with actual state.
	// currently, when an endpoint is in a failed state, we recreate it.
	// To avoid this, we'll always set the endpoint status to running.
	status := &v1.EndpointStatus{
		Phase: v1.EndpointPhaseRUNNING,
	}

	ready := util.IsDeploymentUpdatedAndReady(dep)
	if !ready {
		errorMessage := ""

		for _, condtion := range dep.Status.Conditions {
			if condtion.Status == corev1.ConditionTrue {
				continue
			}

			errorMessage += fmt.Sprintf("Type: %s, Reason: %s, Message: %s; ", condtion.Type, condtion.Reason, condtion.Message)
		}

		errorMessage += fmt.Sprintf("Deployment now: %d/%d replicas ready.", dep.Status.ReadyReplicas, *dep.Spec.Replicas)
		status.ErrorMessage = errorMessage
	}

	return status, nil
}

func (k *kubernetesOrchestrator) ConnectEndpointModel(endpoint *v1.Endpoint) error {
	// Implementation for connecting an endpoint to its model in a Kubernetes cluster
	return nil
}

func (k *kubernetesOrchestrator) DisconnectEndpointModel(endpoint *v1.Endpoint) error {
	// Implementation for disconnecting an endpoint from its model in a Kubernetes cluster
	return nil
}
