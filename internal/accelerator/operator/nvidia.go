package operator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

// NvidiaAcceleratorOperator owns the NVIDIA base components and their
// provider recovery boundary. It intentionally does not own the GPU Operator
// driver/toolkit resources themselves.
type NvidiaAcceleratorOperator struct{ renderer *NvidiaResourceRenderer }

func NewNvidiaAcceleratorOperator(provider publicaccelerator.ProfileProvider) *NvidiaAcceleratorOperator {
	if provider == nil {
		return &NvidiaAcceleratorOperator{}
	}

	return &NvidiaAcceleratorOperator{renderer: NewNvidiaResourceRenderer(provider)}
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
	provider := providerForContext(opCtx)
	state, err := provider.Evaluate(ctx)
	if err != nil {
		return OperatorResult{}, err
	}
	plan := BuildNvidiaComponentPlan(state)
	if n.renderer == nil {
		rebuilt, err := n.rebuildStaleBasePods(ctx, opCtx, state)
		if err != nil {
			return OperatorResult{}, err
		}
		return OperatorResult{Provider: state, Plan: plan, RebuiltPods: rebuilt}, nil
	}

	if opCtx.MetricsTargets != nil {
		targets, err := n.renderer.MetricsTargets(ctx, opCtx)
		if err != nil {
			return OperatorResult{}, fmt.Errorf("resolve NVIDIA metrics targets: %w", err)
		}
		if err := opCtx.MetricsTargets.Replace("operator/nvidia", targets); err != nil {
			return OperatorResult{}, fmt.Errorf("register NVIDIA metrics targets: %w", err)
		}
	}

	nodeAgent, err := n.ReconcileNodeAgent(ctx, opCtx, state, plan)
	if err != nil {
		return OperatorResult{}, err
	}
	exporter, err := n.ReconcileExporter(ctx, opCtx, state, plan)
	if err != nil {
		return OperatorResult{}, err
	}
	virtualization, err := n.ReconcileAcceleratorVirtualization(ctx, opCtx, state, plan)
	if err != nil {
		return OperatorResult{}, err
	}
	resources := &unstructured.UnstructuredList{}
	resources.Items = append(resources.Items, nodeAgent.Items...)
	resources.Items = append(resources.Items, exporter.Items...)
	resources.Items = append(resources.Items, virtualization.Items...)

	rebuilt, err := n.rebuildStaleBasePods(ctx, opCtx, state)
	if err != nil {
		return OperatorResult{}, err
	}

	return OperatorResult{Provider: state, Plan: plan, RebuiltPods: rebuilt, Resources: resources}, nil
}

func (n *NvidiaAcceleratorOperator) ReconcileNodeAgent(ctx context.Context, opCtx *OperatorContext, state ProviderState, plan ComponentPlan) (*unstructured.UnstructuredList, error) {
	resources, err := n.renderer.RenderNodeAgent(ctx, opCtx, state, plan)
	if err != nil {
		return nil, fmt.Errorf("render NVIDIA NodeAgent: %w", err)
	}
	if err := ApplyScopeResources(ctx, opCtx, NvidiaNodeAgentScope, resources); err != nil {
		return nil, fmt.Errorf("apply NVIDIA NodeAgent: %w", err)
	}
	return resources, nil
}

func (n *NvidiaAcceleratorOperator) ReconcileExporter(ctx context.Context, opCtx *OperatorContext, state ProviderState, plan ComponentPlan) (*unstructured.UnstructuredList, error) {
	resources, err := n.renderer.RenderExporter(ctx, opCtx, state, plan)
	if err != nil {
		return nil, fmt.Errorf("render NVIDIA exporter: %w", err)
	}
	if err := ApplyScopeResources(ctx, opCtx, NvidiaExporterScope, resources); err != nil {
		return nil, fmt.Errorf("apply NVIDIA exporter: %w", err)
	}
	return resources, nil
}

func (n *NvidiaAcceleratorOperator) ReconcileAcceleratorVirtualization(ctx context.Context, opCtx *OperatorContext, state ProviderState, plan ComponentPlan) (*unstructured.UnstructuredList, error) {
	if opCtx.Cluster.Spec == nil || !opCtx.Cluster.Spec.AcceleratorVirtualizationEnabled() {
		resources := &unstructured.UnstructuredList{}
		if err := ApplyScopeResources(ctx, opCtx, NvidiaVirtualizationScope, resources); err != nil {
			return nil, fmt.Errorf("remove NVIDIA accelerator virtualization: %w", err)
		}
		if err := cleanupNvidiaNodeScope(ctx, opCtx); err != nil {
			return nil, fmt.Errorf("clean NVIDIA virtualization node scope: %w", err)
		}
		return resources, nil
	}
	config, err := n.renderer.ResolveVirtualizationConfig(ctx, opCtx)
	if err != nil {
		return nil, err
	}
	if config != nil {
		if !config.Supported {
			if len(config.BlockingReasons) > 0 {
				return nil, fmt.Errorf("NVIDIA accelerator virtualization is unsupported: %s", strings.Join(config.BlockingReasons, "; "))
			}
			return nil, fmt.Errorf("NVIDIA accelerator virtualization mode is unsupported")
		}
		if len(config.BlockingReasons) > 0 {
			return nil, fmt.Errorf("accelerator plugin blocked HAMi virtualization: %s", strings.Join(config.BlockingReasons, "; "))
		}
	}
	resources, err := n.renderer.RenderAcceleratorVirtualization(ctx, opCtx, state, plan)
	if err != nil {
		return nil, fmt.Errorf("render NVIDIA accelerator virtualization: %w", err)
	}
	if err := ApplyScopeResources(ctx, opCtx, NvidiaVirtualizationScope, resources); err != nil {
		return nil, fmt.Errorf("apply NVIDIA accelerator virtualization: %w", err)
	}
	if config != nil && config.Supported {
		if err := reconcileNvidiaNodeScope(ctx, opCtx, config); err != nil {
			return nil, fmt.Errorf("reconcile NVIDIA virtualization node scope: %w", err)
		}
	}
	return resources, nil
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

func (n *NvidiaAcceleratorOperator) rebuildStaleBasePods(ctx context.Context, opCtx *OperatorContext, state ProviderState) ([]string, error) {
	if !state.Managed || len(state.NodeStates) == 0 || !validOperatorScope(opCtx) {
		return nil, nil
	}

	pods := &corev1.PodList{}
	if err := opCtx.Client.List(ctx, pods, client.InNamespace(opCtx.Namespace), client.MatchingLabels(operatorScopeLabels(opCtx))); err != nil {
		return nil, fmt.Errorf("list NVIDIA operator base Pods: %w", err)
	}

	stale := make([]string, 0)

	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil || pod.Spec.NodeName == "" {
			continue
		}

		owned, err := currentOperatorDaemonSetPod(ctx, opCtx, pod)
		if err != nil {
			return stale, err
		}

		if !owned {
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

		if err := opCtx.Client.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
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
