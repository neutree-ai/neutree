package operator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/metricsconfig"
)

// NvidiaAcceleratorOperator owns the NVIDIA base components and their
// provider recovery boundary. It intentionally does not own the GPU Operator
// driver/toolkit resources themselves.
type NvidiaAcceleratorOperator struct {
	renderer ResourceRenderer
}

func NewNvidiaAcceleratorOperator(renderers ...ResourceRenderer) *NvidiaAcceleratorOperator {
	var renderer ResourceRenderer
	if len(renderers) > 0 {
		renderer = renderers[0]
	}

	return &NvidiaAcceleratorOperator{renderer: renderer}
}

func (n *NvidiaAcceleratorOperator) Name() string {
	return NVIDIAOperatorName
}

func (n *NvidiaAcceleratorOperator) AcceleratorType() string {
	return NVIDIAAcceleratorType
}

func (n *NvidiaAcceleratorOperator) Reconcile(ctx context.Context, opCtx *OperatorContext) (OperatorResult, error) {
	if opCtx == nil || opCtx.Client == nil {
		return OperatorResult{}, fmt.Errorf("NVIDIA operator context and Kubernetes client are required")
	}

	provider := NewNvidiaProvider(opCtx.Client)

	state, err := provider.Evaluate(ctx)
	if err != nil {
		return OperatorResult{}, err
	}

	plan := BuildNvidiaComponentPlan(state)

	if opCtx.MetricsTargets != nil {
		opCtx.MetricsTargets.Replace("operator/nvidia", nvidiaMetricsTargets(opCtx))
	}

	renderer := n.renderer
	if renderer == nil {
		renderer = opCtx.Renderer
	}
	var resources *unstructured.UnstructuredList

	if renderer != nil {
		if controlPlane, ok := renderer.(ControlPlaneReconciler); ok {
			if err := controlPlane.ReconcileControlPlane(ctx, opCtx); err != nil {
				return OperatorResult{}, fmt.Errorf("reconcile NVIDIA control plane: %w", err)
			}
		}

		resources, err = renderer.Render(ctx, opCtx, state, plan)
		if err != nil {
			return OperatorResult{}, fmt.Errorf("render NVIDIA accelerator resources: %w", err)
		}

		if err := ApplyResources(ctx, opCtx, resources); err != nil {
			return OperatorResult{}, err
		}
	}

	rebuilt, err := n.rebuildStaleBasePods(ctx, opCtx.Client, state)
	if err != nil {
		return OperatorResult{}, err
	}

	return OperatorResult{Provider: state, Plan: plan, RebuiltPods: rebuilt, Resources: resources}, nil
}

func nvidiaMetricsTargets(opCtx *OperatorContext) []metricsconfig.Target {
	namespace := ""
	if opCtx != nil {
		namespace = opCtx.Namespace
	}

	targets := []metricsconfig.Target{
		{
			AcceleratorType: NVIDIAAcceleratorType,
			JobName:         "nvidia-node-agent",
			Kind:            metricsconfig.TargetKindNodeAgent,
			Namespace:       namespace,
			Selector: map[string]string{
				"neutree.ai/metrics-target": "node-agent",
				OperatorOwnerLabel:          OperatorOwnerValue,
				DeploymentModeLabel:         DeploymentModeValue,
			},
			Port:        19101,
			MetricsPath: "/metrics",
		},
		{
			AcceleratorType: NVIDIAAcceleratorType,
			JobName:         "accelerator-exporter-nvidia-gpu",
			Kind:            metricsconfig.TargetKindAcceleratorExporter,
			Namespace:       namespace,
			Selector: map[string]string{
				"neutree.ai/metrics-target":   "accelerator-exporter",
				"neutree.ai/accelerator-type": NVIDIAAcceleratorType,
				OperatorOwnerLabel:            OperatorOwnerValue,
				DeploymentModeLabel:           DeploymentModeValue,
			},
			Port:        19400,
			MetricsPath: "/metrics",
		},
	}
	if opCtx != nil && opCtx.Cluster != nil && opCtx.Cluster.Spec != nil && opCtx.Cluster.Spec.AcceleratorVirtualizationEnabled() {
		targets = append(targets, metricsconfig.Target{
			AcceleratorType: NVIDIAAcceleratorType,
			JobName:         "hami-vgpu-monitor",
			Kind:            metricsconfig.TargetKindVirtualizationMonitor,
			Namespace:       namespace,
			Selector: map[string]string{
				"app.kubernetes.io/component": "hami-device-plugin",
				OperatorOwnerLabel:            OperatorOwnerValue,
				DeploymentModeLabel:           DeploymentModeValue,
			},
			Port:        9394,
			MetricsPath: "/metrics",
		})
	}

	return targets
}

func (n *NvidiaAcceleratorOperator) Status(ctx context.Context, opCtx *OperatorContext) (*v1.ComponentStatus, error) {
	if opCtx == nil || opCtx.Client == nil {
		return nil, fmt.Errorf("NVIDIA operator context and Kubernetes client are required")
	}

	state, err := NewNvidiaProvider(opCtx.Client).Evaluate(ctx)
	if err != nil {
		return nil, err
	}

	phase := v1.ComponentPhaseNotReady
	if state.Ready {
		phase = v1.ComponentPhaseReady
	}

	return &v1.ComponentStatus{
		Phase:   phase,
		Managed: true,
		Version: NVIDIAAcceleratorType,
		Reason:  state.Reason,
		Message: providerStatusMessage(state),
	}, nil
}

func (n *NvidiaAcceleratorOperator) Delete(ctx context.Context, opCtx *OperatorContext) error {
	if opCtx == nil || opCtx.Client == nil {
		return fmt.Errorf("NVIDIA operator context and Kubernetes client are required")
	}

	// Only resources carrying the operator owner label are eligible. Driver,
	// toolkit, Hami, and customer Pods are intentionally outside this boundary.
	dsList := &appsv1.DaemonSetList{}
	if err := opCtx.Client.List(ctx, dsList, client.MatchingLabels{OperatorOwnerLabel: OperatorOwnerValue}); err != nil {
		return err
	}

	for i := range dsList.Items {
		if err := opCtx.Client.Delete(ctx, &dsList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete NVIDIA operator DaemonSet %s/%s: %w", dsList.Items[i].Namespace, dsList.Items[i].Name, err)
		}
	}

	nodes := &corev1.NodeList{}
	if err := opCtx.Client.List(ctx, nodes); err != nil {
		return err
	}

	for i := range nodes.Items {
		node := &nodes.Items[i]
		if node.Annotations == nil {
			continue
		}

		if _, managed := node.Annotations[ClientDeployManagedAnnotation]; !managed {
			continue
		}

		base := node.DeepCopy()
		delete(node.Annotations, ClientDeployManagedAnnotation)

		if err := opCtx.Client.Patch(ctx, node, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("release NVIDIA client label management for node %s: %w", node.Name, err)
		}
	}

	return nil
}

func providerStatusMessage(state ProviderState) string {
	parts := make([]string, 0, len(state.NodeStates)+1)
	if state.Reason != "" {
		parts = append(parts, state.Reason)
	}

	nodes := make([]string, 0, len(state.NodeStates))

	for node, nodeState := range state.NodeStates {
		if nodeState.Reason != "" {
			nodes = append(nodes, fmt.Sprintf("%s: %s", node, nodeState.Reason))
		}
	}

	sort.Strings(nodes)

	if len(nodes) > 0 {
		parts = append(parts, "nodes="+strings.Join(nodes, "; "))
	}

	if len(parts) > 0 {
		return strings.Join(parts, "; ")
	}

	if !state.Managed {
		return "NVIDIA driver and container toolkit are managed outside GPU Operator"
	}

	if !state.Ready {
		return "NVIDIA provider is not ready"
	}

	return "NVIDIA provider and accelerator operator are ready"
}

// BuildNvidiaComponentPlan returns the placement/ownership metadata consumed
// by the concrete resource renderer. Operator-managed provider nodes use the
// independent gate; OS/platform nodes retain the legacy placement path.
func BuildNvidiaComponentPlan(state ProviderState) ComponentPlan {
	ownerLabels := map[string]string{
		OperatorOwnerLabel:  OperatorOwnerValue,
		DeploymentModeLabel: DeploymentModeValue,
	}

	nodeSelector := map[string]string{}
	if state.Managed {
		nodeSelector[ClientDeployLabel] = NVIDIAGPUNodeValue
	}

	names := []struct {
		name string
		kind string
	}{
		{name: "nvidia-node-agent", kind: "DaemonSet"},
		{name: "nvidia-dcgm-exporter", kind: "DaemonSet"},
		{name: "nvidia-device-plugin", kind: "DaemonSet"},
	}
	components := make([]ComponentSpec, 0, len(names))

	for _, item := range names {
		components = append(components, ComponentSpec{
			Name:         item.name,
			Kind:         item.kind,
			NodeSelector: cloneStringMap(nodeSelector),
			OwnerLabels:  cloneStringMap(ownerLabels),
		})
	}

	return ComponentPlan{
		AcceleratorType: NVIDIAAcceleratorType,
		Managed:         state.Managed,
		NodeSelector:    nodeSelector,
		OwnerLabels:     ownerLabels,
		Components:      components,
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}

	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}

	return result
}

func (n *NvidiaAcceleratorOperator) rebuildStaleBasePods(ctx context.Context, ctrlClient client.Client, state ProviderState) ([]string, error) {
	if !state.Managed || len(state.NodeStates) == 0 {
		return nil, nil
	}

	pods := &corev1.PodList{}
	if err := ctrlClient.List(ctx, pods, client.MatchingLabels{OperatorOwnerLabel: OperatorOwnerValue}); err != nil {
		return nil, fmt.Errorf("list NVIDIA operator base Pods: %w", err)
	}

	stale := make([]string, 0)

	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil || pod.Spec.NodeName == "" {
			continue
		}

		nodeState, ok := state.NodeStates[pod.Spec.NodeName]
		if !ok || !nodeState.ProviderStartKnown {
			continue
		}

		decision := BasePodStartDecisionFor(pod, nodeState.ProviderLastContainerStart)
		if decision != BasePodStartRebuild {
			continue
		}

		if err := ctrlClient.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			return stale, fmt.Errorf("delete stale NVIDIA operator Pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}

		stale = append(stale, pod.Namespace+"/"+pod.Name)
	}

	sort.Strings(stale)

	return stale, nil
}

// NewNvidiaBasePod is a small helper used by renderers and tests to attach the
// correct ownership labels without mutating caller maps.
func NewNvidiaBasePod(name, namespace string, nodeSelector map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				OperatorOwnerLabel:  OperatorOwnerValue,
				DeploymentModeLabel: DeploymentModeValue,
			},
		},
		Spec: corev1.PodSpec{NodeSelector: cloneStringMap(nodeSelector)},
	}
}
