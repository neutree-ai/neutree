package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/operator/manifests"
	"github.com/neutree-ai/neutree/internal/deploy"
)

const ClientDeployOwnerAnnotation = "neutree.ai/gpu.deploy.client.owner"

func validOperatorScope(opCtx *OperatorContext) bool {
	return opCtx != nil && opCtx.Client != nil && opCtx.Cluster != nil && opCtx.Cluster.Metadata != nil && opCtx.Cluster.GetName() != "" && opCtx.Namespace != ""
}

func operatorScopeLabels(opCtx *OperatorContext) map[string]string {
	return map[string]string{
		OperatorOwnerLabel: OperatorOwnerValue, DeploymentModeLabel: DeploymentModeValue,
		v1.NeutreeClusterLabelKey: opCtx.Cluster.GetName(), v1.NeutreeClusterWorkspaceLabelKey: opCtx.Cluster.GetWorkspace(),
	}
}

func providerForContext(opCtx *OperatorContext) *NvidiaProvider {
	provider := NewNvidiaProvider(opCtx.Client)
	if validOperatorScope(opCtx) {
		provider.Owner = opCtx.Cluster.GetWorkspace() + "/" + opCtx.Cluster.GetName()
		provider.Namespace = opCtx.Namespace
		provider.OwnerLabels = operatorScopeLabels(opCtx)
	}

	return provider
}

func operatorInventory(ctx context.Context, opCtx *OperatorContext) ([]unstructured.Unstructured, error) {
	components := []string{
		nvidiaScopeComponent(NvidiaNodeAgentScope), nvidiaScopeComponent(NvidiaExporterScope),
		nvidiaScopeComponent(NvidiaVirtualizationScope), "accelerator-operator-" + opCtx.Cluster.GetName(),
	}
	var all []unstructured.Unstructured
	for _, component := range components {
		data, err := deploy.NewConfigStore(opCtx.Client).Get(ctx, opCtx.Namespace, opCtx.Cluster.GetName(), component)
		if err != nil {
			return nil, err
		}
		if data == "" {
			continue
		}

		var objects []unstructured.Unstructured
		if err := json.Unmarshal([]byte(data), &objects); err != nil {
			return nil, err
		}
		all = append(all, objects...)
	}

	return all, nil
}

func operatorInventoryScope(ctx context.Context, opCtx *OperatorContext, scope string) ([]unstructured.Unstructured, error) {
	data, err := deploy.NewConfigStore(opCtx.Client).Get(ctx, opCtx.Namespace, opCtx.Cluster.GetName(), nvidiaScopeComponent(scope))
	if err != nil || data == "" {
		return nil, err
	}

	var objects []unstructured.Unstructured
	if err := json.Unmarshal([]byte(data), &objects); err != nil {
		return nil, err
	}

	return objects, nil
}

func scopedOperatorObject(opCtx *OperatorContext, object client.Object) bool {
	if object.GetNamespace() != "" && object.GetNamespace() != opCtx.Namespace {
		return false
	}

	for key, value := range operatorScopeLabels(opCtx) {
		if object.GetLabels()[key] != value {
			return false
		}
	}

	return true
}

func (n *NvidiaAcceleratorOperator) Status(ctx context.Context, opCtx *OperatorContext) (*v1.ComponentStatus, error) {
	if !validOperatorScope(opCtx) {
		return nil, fmt.Errorf("NVIDIA operator cluster scope and Kubernetes client are required")
	}

	state, err := providerForContext(opCtx).Evaluate(ctx)
	if err != nil {
		return nil, err
	}

	status := &v1.ComponentStatus{
		Phase: v1.ComponentPhaseNotReady, Managed: true, Version: NVIDIAAcceleratorType,
		Reason: state.Reason, Message: providerStatusMessage(state),
	}
	if !state.Ready {
		return status, nil
	}

	objects, err := operatorInventory(ctx, opCtx)
	if err != nil {
		return nil, fmt.Errorf("read NVIDIA operator inventory: %w", err)
	}

	nodes := &corev1.NodeList{}
	if err := opCtx.Client.List(ctx, nodes, client.MatchingLabels{NVIDIAGPUNodeLabel: NVIDIAGPUNodeValue}); err != nil {
		return nil, err
	}

	if len(nodes.Items) == 0 {
		status.Reason = "NoGPUNodes"
		status.Message = "NVIDIA operator has no discovered GPU nodes"

		return status, nil
	}

	for _, node := range nodes.Items {
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue {
				status.Reason = "NodeNotReady"
				status.Message = "NVIDIA GPU node " + node.Name + " is not Ready"

				return status, nil
			}
		}
	}

	var failures []string
	noScheduledPods := false
	workloadCount := 0
	hasNodeAgent := false
	newScopes, err := n.hasScopeInventories(ctx, opCtx)
	if err != nil {
		return nil, err
	}
	if newScopes {
		for _, check := range []struct {
			name string
			fn   func(context.Context, *OperatorContext) (bool, error)
		}{
			{name: "NodeAgent", fn: n.StatusNodeAgent},
			{name: "Exporter", fn: n.StatusExporter},
			{name: "Virtualization", fn: n.StatusAcceleratorVirtualization},
		} {
			ready, err := check.fn(ctx, opCtx)
			if err != nil {
				return nil, err
			}
			if !ready {
				failures = append(failures, "NVIDIA "+check.name+" scope is not ready")
			}
		}
	}
	if n.renderer != nil && opCtx.Cluster.Spec != nil && opCtx.Cluster.Spec.AcceleratorVirtualizationEnabled() {
		config, err := n.renderer.ResolveVirtualizationConfig(ctx, opCtx)
		if err != nil {
			return nil, err
		}
		if config != nil && config.Supported {
			for _, nodeName := range config.CandidateNodes {
				node := &corev1.Node{}
				if err := opCtx.Client.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
					if apierrors.IsNotFound(err) {
						failures = append(failures, "virtualization node "+nodeName+" is missing")
						continue
					}
					return nil, err
				}

				if node.Labels[config.NodeScopeLabel.Key] != config.NodeScopeLabel.EnabledValue {
					failures = append(failures, "virtualization node "+nodeName+" is not enabled")
				}
			}
		}
	}

	for _, object := range objects {
		if object.GetKind() != daemonSetKind && object.GetKind() != "Deployment" {
			continue
		}

		workloadCount++

		if object.GetKind() == daemonSetKind && object.GetName() == manifests.NodeAgentName {
			hasNodeAgent = true
		}

		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(object.GroupVersionKind())

		if err := opCtx.Client.Get(ctx, client.ObjectKeyFromObject(&object), live); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, err
			}

			failures = append(failures, object.GetKind()+"/"+object.GetName()+" is missing")

			continue
		}

		if !scopedOperatorObject(opCtx, live) {
			failures = append(failures, object.GetKind()+"/"+object.GetName()+" ownership does not match")
			continue
		}

		ready, err := operatorWorkloadReady(live)
		if err != nil {
			return nil, err
		}

		if object.GetKind() == daemonSetKind && len(nodes.Items) > 0 {
			desired, _, err := unstructured.NestedInt64(live.Object, "status", "desiredNumberScheduled")
			if err != nil {
				return nil, err
			}

			if desired == 0 {
				noScheduledPods = true

				failures = append(failures, object.GetKind()+"/"+object.GetName()+" has no scheduled Pods on discovered NVIDIA GPU nodes")

				continue
			}
		}

		if !ready {
			failures = append(failures, object.GetKind()+"/"+object.GetName()+" is not ready")
		}
	}

	if workloadCount == 0 || !hasNodeAgent {
		failures = append(failures, "NVIDIA operator workload inventory is missing")
	}

	if len(failures) > 0 {
		sort.Strings(failures)

		status.Reason = "BaseComponentsNotReady"
		if noScheduledPods {
			status.Reason = "NoScheduledBasePods"
		}

		status.Message = strings.Join(failures, "; ")

		return status, nil
	}

	status.Phase = v1.ComponentPhaseReady
	status.Reason = ""
	status.Message = "NVIDIA provider and accelerator operator components are ready"
	status.Message += runtimeClassDiagnostic(state)

	return status, nil
}

// StatusNodeAgent checks only the NodeAgent scope inventory and workload.
func (n *NvidiaAcceleratorOperator) StatusNodeAgent(ctx context.Context, opCtx *OperatorContext) (bool, error) {
	objects, err := operatorInventoryScope(ctx, opCtx, NvidiaNodeAgentScope)
	if err != nil {
		return false, err
	}
	return statusForNamedWorkload(ctx, opCtx, objects, manifests.NodeAgentName), nil
}

// StatusExporter checks the managed exporter scope. An empty scope is valid
// when the profile selects an external exporter.
func (n *NvidiaAcceleratorOperator) StatusExporter(ctx context.Context, opCtx *OperatorContext) (bool, error) {
	objects, err := operatorInventoryScope(ctx, opCtx, NvidiaExporterScope)
	if err != nil {
		return false, err
	}
	if len(objects) == 0 {
		return true, nil
	}
	return statusForNamedWorkload(ctx, opCtx, objects, manifests.ExporterName), nil
}

// StatusAcceleratorVirtualization checks the device-plugin workload and all
// provider candidate nodes' virtualization labels.
func (n *NvidiaAcceleratorOperator) StatusAcceleratorVirtualization(ctx context.Context, opCtx *OperatorContext) (bool, error) {
	if opCtx.Cluster.Spec == nil || !opCtx.Cluster.Spec.AcceleratorVirtualizationEnabled() {
		return true, nil
	}

	objects, err := operatorInventoryScope(ctx, opCtx, NvidiaVirtualizationScope)
	if err != nil {
		return false, err
	}
	if !statusForNamedWorkload(ctx, opCtx, objects, "hami-device-plugin") {
		return false, nil
	}

	if n.renderer == nil {
		return true, nil
	}
	config, err := n.renderer.ResolveVirtualizationConfig(ctx, opCtx)
	if err != nil {
		return false, err
	}
	if config == nil || !config.Supported {
		return false, nil
	}
	for _, name := range config.CandidateNodes {
		node := &corev1.Node{}
		if err := opCtx.Client.Get(ctx, client.ObjectKey{Name: name}, node); err != nil {
			return false, err
		}
		if node.Labels[config.NodeScopeLabel.Key] != config.NodeScopeLabel.EnabledValue {
			return false, nil
		}
	}

	return true, nil
}

func statusForNamedWorkload(ctx context.Context, opCtx *OperatorContext, objects []unstructured.Unstructured, name string) bool {
	for _, object := range objects {
		if object.GetName() != name || object.GetKind() != daemonSetKind {
			continue
		}
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(object.GroupVersionKind())
		if err := opCtx.Client.Get(ctx, client.ObjectKeyFromObject(&object), live); err != nil {
			return false
		}
		ready, err := operatorWorkloadReady(live)
		return err == nil && ready
	}

	return false
}

func operatorWorkloadReady(object *unstructured.Unstructured) (bool, error) {
	if object.GetDeletionTimestamp() != nil {
		return false, nil
	}

	if object.GetKind() == daemonSetKind {
		ds := &appsv1.DaemonSet{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, ds); err != nil {
			return false, err
		}

		desired := ds.Status.DesiredNumberScheduled

		return ds.Status.ObservedGeneration >= ds.Generation && ds.Status.NumberReady == desired &&
			ds.Status.NumberAvailable == desired && ds.Status.UpdatedNumberScheduled == desired && ds.Status.NumberUnavailable == 0, nil
	}

	deployment := &appsv1.Deployment{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, deployment); err != nil {
		return false, err
	}

	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}

	return deployment.Status.ObservedGeneration >= deployment.Generation && deployment.Status.UpdatedReplicas == desired &&
		deployment.Status.ReadyReplicas == desired && deployment.Status.AvailableReplicas == desired && deployment.Status.UnavailableReplicas == 0, nil
}

func (n *NvidiaAcceleratorOperator) Delete(ctx context.Context, opCtx *OperatorContext) error {
	if !validOperatorScope(opCtx) {
		return fmt.Errorf("NVIDIA operator cluster scope and Kubernetes client are required")
	}

	newScopes, err := n.hasScopeInventories(ctx, opCtx)
	if err != nil {
		return err
	}
	if newScopes {
		if err := n.DeleteAcceleratorVirtualization(ctx, opCtx); err != nil {
			return err
		}
		if err := n.DeleteExporter(ctx, opCtx); err != nil {
			return err
		}
		if err := n.DeleteNodeAgent(ctx, opCtx); err != nil {
			return err
		}

		if opCtx.MetricsTargets != nil {
			opCtx.MetricsTargets.Remove("operator/nvidia")
		}

		for _, component := range []string{
			nvidiaScopeComponent(NvidiaNodeAgentScope), nvidiaScopeComponent(NvidiaExporterScope), nvidiaScopeComponent(NvidiaVirtualizationScope),
		} {
			if err := deploy.NewConfigStore(opCtx.Client).Delete(ctx, opCtx.Namespace, opCtx.Cluster.GetName(), component); err != nil {
				return err
			}
		}

		return nil
	}

	return n.deleteLegacyOperatorResources(ctx, opCtx)
}

// DeleteNodeAgent removes the NodeAgent scope and waits for writer Pods before
// cleaning their persisted node annotation scope.
func (n *NvidiaAcceleratorOperator) DeleteNodeAgent(ctx context.Context, opCtx *OperatorContext) error {
	component := nvidiaScopeComponent(NvidiaNodeAgentScope)
	inventoryName := "neutree-" + opCtx.Cluster.GetName() + "-" + component + "-config"
	cleanup, writersPending, err := captureOperatorNodeCleanup(ctx, opCtx, inventoryName)
	if err != nil {
		return err
	}
	deleted, err := deleteOperatorScope(ctx, opCtx, NvidiaNodeAgentScope)
	if err != nil {
		return err
	}
	if !deleted || writersPending {
		return fmt.Errorf("NVIDIA NodeAgent resources are still deleting")
	}

	return cleanupOperatorNodeAnnotations(ctx, opCtx, cleanup.Nodes)
}

// DeleteExporter removes the managed exporter scope.
func (n *NvidiaAcceleratorOperator) DeleteExporter(ctx context.Context, opCtx *OperatorContext) error {
	deleted, err := deleteOperatorScope(ctx, opCtx, NvidiaExporterScope)
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("NVIDIA exporter resources are still deleting")
	}

	return nil
}

// DeleteAcceleratorVirtualization removes the NVIDIA device-plugin scope and
// its virtualization labels. Hami control-plane resources remain untouched.
func (n *NvidiaAcceleratorOperator) DeleteAcceleratorVirtualization(ctx context.Context, opCtx *OperatorContext) error {
	deleted, err := deleteOperatorScope(ctx, opCtx, NvidiaVirtualizationScope)
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("NVIDIA virtualization resources are still deleting")
	}

	if n.renderer != nil {
		config, err := n.renderer.ResolveVirtualizationConfig(ctx, opCtx)
		if err != nil {
			return err
		}
		if config != nil {
			return cleanupNvidiaNodeScope(ctx, opCtx)
		}
	}

	return nil
}

func deleteOperatorScope(ctx context.Context, opCtx *OperatorContext, scope string) (bool, error) {
	return deploy.NewKubernetesDeployer(opCtx.Client, opCtx.Namespace, opCtx.Cluster.GetName(), nvidiaScopeComponent(scope)).Delete(ctx)
}

func (n *NvidiaAcceleratorOperator) hasScopeInventories(ctx context.Context, opCtx *OperatorContext) (bool, error) {
	store := deploy.NewConfigStore(opCtx.Client)
	for _, scope := range []string{NvidiaNodeAgentScope, NvidiaExporterScope, NvidiaVirtualizationScope} {
		data, err := store.Get(ctx, opCtx.Namespace, opCtx.Cluster.GetName(), nvidiaScopeComponent(scope))
		if err != nil {
			return false, err
		}
		if data != "" {
			return true, nil
		}
	}

	return false, nil
}

func (n *NvidiaAcceleratorOperator) deleteLegacyOperatorResources(ctx context.Context, opCtx *OperatorContext) error {
	if !validOperatorScope(opCtx) {
		return fmt.Errorf("NVIDIA operator cluster scope and Kubernetes client are required")
	}

	objects, err := operatorInventory(ctx, opCtx)
	if err != nil {
		return err
	}

	inventoryName := "neutree-" + opCtx.Cluster.GetName() + "-" + nvidiaScopeComponent(NvidiaNodeAgentScope) + "-config"

	cleanup, writersPending, err := captureOperatorNodeCleanup(ctx, opCtx, inventoryName)
	if err != nil {
		return err
	}
	// Include labelled resources even when a previous apply only partially saved its inventory.
	kinds := []schema.GroupVersionKind{
		{Version: "v1", Kind: "ConfigMap"},
		{Version: "v1", Kind: "ServiceAccount"},
		{Version: "v1", Kind: "Secret"},
		{Version: "v1", Kind: "Service"},
		{Group: "apps", Version: "v1", Kind: daemonSetKind},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"},
		{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "MutatingWebhookConfiguration"},
		{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingWebhookConfiguration"},
	}
	for _, gvk := range kinds {
		if !opCtx.Client.Scheme().Recognizes(gvk) {
			continue
		}

		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))

		if err := opCtx.Client.List(ctx, list, client.MatchingLabels(operatorScopeLabels(opCtx))); err != nil {
			return err
		}

		for _, object := range list.Items {
			if object.GetName() != inventoryName && scopedOperatorObject(opCtx, &object) {
				objects = append(objects, object)
			}
		}
	}

	pending := false

	for _, object := range objects {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(object.GroupVersionKind())

		if err := opCtx.Client.Get(ctx, client.ObjectKeyFromObject(&object), live); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}

			return err
		}

		if !scopedOperatorObject(opCtx, live) {
			continue
		}

		pending = true

		if live.GetDeletionTimestamp() == nil {
			if err := opCtx.Client.Delete(ctx, live); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	if opCtx.MetricsTargets != nil {
		opCtx.MetricsTargets.Remove("operator/nvidia")
	}

	if pending || writersPending {
		return fmt.Errorf("NVIDIA operator resources are still deleting")
	}

	if err := cleanupNvidiaNodeScope(ctx, opCtx); err != nil {
		return fmt.Errorf("clean NVIDIA virtualization node scope: %w", err)
	}

	if err := cleanupOperatorNodeAnnotations(ctx, opCtx, cleanup.Nodes); err != nil {
		return err
	}

	for _, component := range []string{
		nvidiaScopeComponent(NvidiaNodeAgentScope), nvidiaScopeComponent(NvidiaExporterScope),
		nvidiaScopeComponent(NvidiaVirtualizationScope), "accelerator-operator-" + opCtx.Cluster.GetName(),
	} {
		if err := deploy.NewConfigStore(opCtx.Client).Delete(ctx, opCtx.Namespace, opCtx.Cluster.GetName(), component); err != nil {
			return err
		}
	}

	return nil
}

func currentOperatorDaemonSetPod(ctx context.Context, opCtx *OperatorContext, pod *corev1.Pod) (bool, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != daemonSetKind || owner.UID == "" {
		return false, nil
	}

	ds := &appsv1.DaemonSet{}
	if err := opCtx.Client.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: owner.Name}, ds); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return ds.UID == owner.UID && scopedOperatorObject(opCtx, ds), nil
}
