package operator

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type NvidiaAcceleratorOperator struct{}

func NewNvidiaAcceleratorOperator() *NvidiaAcceleratorOperator {
	return &NvidiaAcceleratorOperator{}
}

func (n *NvidiaAcceleratorOperator) Name() string            { return NVIDIAOperatorName }
func (n *NvidiaAcceleratorOperator) AcceleratorType() string { return NVIDIAAcceleratorType }

func (n *NvidiaAcceleratorOperator) Reconcile(ctx context.Context, opCtx *OperatorContext) (OperatorResult, error) {
	// Metrics and HAMi own workload deployment; this Operator only synchronizes
	// NVIDIA GPU Operator compatibility state on matching nodes.
	if err := validateContext(opCtx); err != nil {
		return OperatorResult{}, err
	}
	if !n.driverManaged(ctx, opCtx) {
		return OperatorResult{}, nil
	}

	nodes := &corev1.NodeList{}
	if err := opCtx.Client.List(ctx, nodes, client.MatchingLabels{NVIDIAGPUNodeLabel: NVIDIAGPUNodeValue}); err != nil {
		return OperatorResult{}, err
	}

	for index := range nodes.Items {
		node := &nodes.Items[index]
		if owner := node.Annotations[NVIDIAOperatorOwnerAnnotation]; owner != "" && owner != NVIDIAOperatorOwnerValue {
			continue
		}
		updated := node.DeepCopy()
		changed := setOperatorAnnotation(updated)
		if setClientLabel(updated, n.driverReady(ctx, opCtx, node.Name)) {
			changed = true
		}
		if changed {
			if err := opCtx.Client.Patch(ctx, updated, client.MergeFrom(node)); err != nil {
				return OperatorResult{}, err
			}
		}
	}

	return OperatorResult{}, nil
}

func (n *NvidiaAcceleratorOperator) Status(ctx context.Context, opCtx *OperatorContext) (*v1.ComponentStatus, error) {
	if err := validateContext(opCtx); err != nil {
		return nil, err
	}

	status := &v1.ComponentStatus{Phase: v1.ComponentPhaseReady, Managed: true, Version: NVIDIAAcceleratorType}
	nodes := &corev1.NodeList{}
	if err := opCtx.Client.List(ctx, nodes, client.MatchingLabels{NVIDIAGPUNodeLabel: NVIDIAGPUNodeValue}); err != nil {
		return nil, err
	}

	var failures []string
	for _, node := range nodes.Items {
		if !n.driverReady(ctx, opCtx, node.Name) && n.driverManaged(ctx, opCtx) {
			failures = append(failures, n.driverFailure(ctx, opCtx, node.Name))
		}
	}

	if len(failures) > 0 {
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
	for index := range nodes.Items {
		node := &nodes.Items[index]
		if node.Annotations[NVIDIAOperatorOwnerAnnotation] != NVIDIAOperatorOwnerValue {
			continue
		}
		updated := node.DeepCopy()
		delete(updated.Annotations, NVIDIAOperatorOwnerAnnotation)
		delete(updated.Labels, NVIDIAGPUClientLabel)
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

func setOperatorAnnotation(node *corev1.Node) bool {
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	if node.Annotations[NVIDIAOperatorOwnerAnnotation] == NVIDIAOperatorOwnerValue {
		return false
	}
	node.Annotations[NVIDIAOperatorOwnerAnnotation] = NVIDIAOperatorOwnerValue
	return true
}

func setClientLabel(node *corev1.Node, ready bool) bool {
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	desired := "false"
	if ready {
		desired = "true"
	}
	if node.Labels[NVIDIAGPUClientLabel] == desired {
		return false
	}
	node.Labels[NVIDIAGPUClientLabel] = desired
	return true
}

func (n *NvidiaAcceleratorOperator) driverManaged(ctx context.Context, opCtx *OperatorContext) bool {
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(schema.GroupVersionKind{Group: "nvidia.com", Version: "v1", Kind: "ClusterPolicy"})
	if err := opCtx.Client.Get(ctx, client.ObjectKey{Name: NVIDIAClusterPolicyName}, policy); err != nil {
		return false
	}
	enabled, found, err := unstructured.NestedBool(policy.Object, "spec", "driver", "enabled")
	return err == nil && (!found || enabled)
}

func (n *NvidiaAcceleratorOperator) driverReady(ctx context.Context, opCtx *OperatorContext, nodeName string) bool {
	pods := &corev1.PodList{}
	if err := opCtx.Client.List(ctx, pods, client.MatchingLabels{NVIDIADriverDeployLabel: "true"}); err != nil {
		return false
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != nodeName {
			continue
		}
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		ready := true
		for _, container := range pod.Status.ContainerStatuses {
			if !container.Ready {
				ready = false
			}
		}
		if ready {
			return true
		}
	}
	return false
}

func (n *NvidiaAcceleratorOperator) driverFailure(ctx context.Context, opCtx *OperatorContext, nodeName string) string {
	pods := &corev1.PodList{}
	if err := opCtx.Client.List(ctx, pods, client.MatchingLabels{NVIDIADriverDeployLabel: "true"}); err != nil {
		return fmt.Sprintf("node %s driver Pod status unavailable: %v; NVIDIA driver and driver libraries may be unavailable", nodeName, err)
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == nodeName {
			return fmt.Sprintf("node %s driver Pod %s is not Ready; NVIDIA driver and driver libraries may be unavailable", nodeName, pod.Name)
		}
	}
	return fmt.Sprintf("node %s has no driver Pod; NVIDIA driver and driver libraries may be unavailable", nodeName)
}
