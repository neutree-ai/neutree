package operator

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/pkg/accelerator"
)

type NvidiaAcceleratorOperator struct{}

func NewNvidiaAcceleratorOperator() *NvidiaAcceleratorOperator { return &NvidiaAcceleratorOperator{} }
func (n *NvidiaAcceleratorOperator) Name() string              { return accelerator.NvidiaOperatorName }
func (n *NvidiaAcceleratorOperator) AcceleratorType() string {
	return accelerator.NvidiaAcceleratorType
}

func (n *NvidiaAcceleratorOperator) Reconcile(ctx context.Context, opCtx *OperatorContext) error {
	if err := validateContext(opCtx); err != nil {
		return err
	}

	policies, err := accelerator.ListGPUOperatorClusterPolicies(ctx, opCtx.Client)
	if err != nil {
		return err
	}

	managed, _, err := accelerator.NvidiaOperatorRuntime(policies)
	if err != nil || !managed {
		return err
	}

	if nativeNvidiaClientManagement(policies) {
		return nil
	}

	pods, err := nvidiaDriverPods(ctx, opCtx.Client)
	if err != nil {
		return err
	}

	nodes := &corev1.NodeList{}
	if err := opCtx.Client.List(ctx, nodes, client.MatchingLabels{
		accelerator.NvidiaGPUDiscoveryLabelKey: accelerator.NvidiaGPUDiscoveryLabelValue,
	}); err != nil {
		return err
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
			return err
		}
	}

	return nil
}

func (n *NvidiaAcceleratorOperator) Status(ctx context.Context, opCtx *OperatorContext) error {
	if err := validateContext(opCtx); err != nil {
		return err
	}

	policies, err := accelerator.ListGPUOperatorClusterPolicies(ctx, opCtx.Client)
	if err != nil {
		return err
	}

	managed, _, err := accelerator.NvidiaOperatorRuntime(policies)
	if err != nil || !managed {
		return err
	}

	pods, err := nvidiaDriverPods(ctx, opCtx.Client)
	if err != nil {
		return err
	}

	nodes := &corev1.NodeList{}
	if err := opCtx.Client.List(ctx, nodes, client.MatchingLabels{
		accelerator.NvidiaGPUDiscoveryLabelKey: accelerator.NvidiaGPUDiscoveryLabelValue,
	}); err != nil {
		return err
	}
	var failures []error

	for _, node := range nodes.Items {
		pod := driverPodForNode(pods, node.Name)
		if pod == nil {
			failures = append(failures, fmt.Errorf("node %s has no driver Pod; NVIDIA driver and driver libraries may be unavailable", node.Name))
			continue
		}

		if detail := driverPodFailure(pod); detail != "" {
			failures = append(failures, fmt.Errorf("node %s driver Pod %s is not ready: %s; NVIDIA driver and driver libraries may be unavailable", node.Name, pod.Name, detail))
		}
	}

	return utilerrors.NewAggregate(failures)
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
