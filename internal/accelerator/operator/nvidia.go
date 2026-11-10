package operator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

type NvidiaAcceleratorOperator struct{}

func NewNvidiaAcceleratorOperator() *NvidiaAcceleratorOperator { return &NvidiaAcceleratorOperator{} }
func (n *NvidiaAcceleratorOperator) Name() string              { return accelerator.NvidiaOperatorName }
func (n *NvidiaAcceleratorOperator) AcceleratorType() string {
	return accelerator.NvidiaAcceleratorType
}

func (n *NvidiaAcceleratorOperator) Reconcile(ctx context.Context, opCtx *OperatorContext) (OperatorResult, error) {
	if err := validateContext(opCtx); err != nil {
		return OperatorResult{}, err
	}

	policies, err := accelerator.ListGPUOperatorClusterPolicies(ctx, opCtx.Client)
	if err != nil {
		return OperatorResult{}, err
	}

	managed, _, err := accelerator.NvidiaOperatorRuntime(policies)
	if err != nil || !managed {
		return OperatorResult{}, err
	}

	native, err := nativeNvidiaClientManagement(ctx, opCtx.Client, policies)
	if err != nil || native {
		return OperatorResult{}, err
	}

	pods, err := nvidiaDriverPods(ctx, opCtx.Client)
	if err != nil {
		return OperatorResult{}, err
	}

	nodes := &corev1.NodeList{}
	if err := opCtx.Client.List(ctx, nodes, client.MatchingLabels{
		accelerator.NvidiaGPUDiscoveryLabelKey: accelerator.NvidiaGPUDiscoveryLabelValue,
	}); err != nil {
		return OperatorResult{}, err
	}

	// Metrics/HAMi own workloads and virtualization labels. Only opted-in
	// client labels participate in the GPU Operator compatibility lifecycle.
	for i := range nodes.Items {
		node := &nodes.Items[i]
		pod := driverPodForNode(pods, node.Name)
		updated := node.DeepCopy()

		if !reconcileClientLabel(updated, pod != nil && pod.Status.Phase == corev1.PodRunning) {
			continue
		}

		if err := opCtx.Client.Patch(ctx, updated, client.MergeFrom(node)); err != nil {
			return OperatorResult{}, err
		}
	}

	return OperatorResult{}, nil
}

func (n *NvidiaAcceleratorOperator) Status(ctx context.Context, opCtx *OperatorContext) (*v1.ComponentStatus, error) {
	if err := validateContext(opCtx); err != nil {
		return nil, err
	}

	status := &v1.ComponentStatus{Phase: v1.ComponentPhaseReady, Managed: true, Version: accelerator.NvidiaAcceleratorType}

	policies, err := accelerator.ListGPUOperatorClusterPolicies(ctx, opCtx.Client)
	if err != nil {
		return nil, err
	}

	managed, _, err := accelerator.NvidiaOperatorRuntime(policies)
	if err != nil || !managed {
		return status, err
	}

	pods, err := nvidiaDriverPods(ctx, opCtx.Client)
	if err != nil {
		return nil, err
	}

	nodes := &corev1.NodeList{}
	if err := opCtx.Client.List(ctx, nodes, client.MatchingLabels{
		accelerator.NvidiaGPUDiscoveryLabelKey: accelerator.NvidiaGPUDiscoveryLabelValue,
	}); err != nil {
		return nil, err
	}
	var failures []string

	for _, node := range nodes.Items {
		pod := driverPodForNode(pods, node.Name)
		if pod == nil {
			failures = append(failures, fmt.Sprintf("node %s has no driver Pod; NVIDIA driver and driver libraries may be unavailable", node.Name))
			continue
		}

		if detail := driverPodFailure(pod); detail != "" {
			failures = append(failures, fmt.Sprintf("node %s driver Pod %s is not ready: %s; NVIDIA driver and driver libraries may be unavailable", node.Name, pod.Name, detail))
		}
	}

	if len(failures) > 0 {
		sort.Strings(failures)

		status.Phase = v1.ComponentPhaseNotReady
		status.Reason = "DriverNotReady"
		status.Message = strings.Join(failures, "; ")
	}

	return status, nil
}

func (n *NvidiaAcceleratorOperator) Delete(ctx context.Context, opCtx *OperatorContext) error {
	if err := validateContext(opCtx); err != nil {
		return err
	}

	nodes := &corev1.NodeList{}
	if err := opCtx.Client.List(ctx, nodes); err != nil {
		return err
	}

	for i := range nodes.Items {
		node := &nodes.Items[i]
		if _, exists := node.Annotations[accelerator.NeutreeOwnerAnnotation]; !exists {
			continue
		}

		updated := node.DeepCopy()
		// The shared client label remains usable by external components after
		// Neutree relinquishes management, including on GPU Operator 26.7+.
		delete(updated.Annotations, accelerator.NeutreeOwnerAnnotation)

		if err := opCtx.Client.Patch(ctx, updated, client.MergeFrom(node)); err != nil {
			return err
		}
	}

	return nil
}

func validateContext(opCtx *OperatorContext) error {
	if opCtx == nil || opCtx.Client == nil || opCtx.Cluster == nil {
		return fmt.Errorf("accelerator operator context and client are required")
	}

	return nil
}

func reconcileClientLabel(node *corev1.Node, running bool) bool {
	_, labelled := node.Labels[accelerator.NvidiaGPUClientLabel]
	annotation, annotated := node.Annotations[accelerator.NeutreeOwnerAnnotation]

	if labelled != annotated || (annotated && annotation != accelerator.NeutreeOwnerValue) {
		return false
	}

	desired := "false"
	if running {
		desired = accelerator.NvidiaGPUDiscoveryLabelValue
	}

	if labelled && node.Labels[accelerator.NvidiaGPUClientLabel] == desired {
		return false
	}

	if node.Labels == nil {
		node.Labels = map[string]string{}
	}

	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}

	node.Labels[accelerator.NvidiaGPUClientLabel] = desired
	node.Annotations[accelerator.NeutreeOwnerAnnotation] = accelerator.NeutreeOwnerValue

	return true
}
