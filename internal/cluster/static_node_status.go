package cluster

import (
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func NeedsStaticNodeRunner(node *v1.StaticNode, reconciler *StaticNodeReconciler) bool {
	if node == nil || node.Spec == nil {
		return false
	}

	if node.Metadata != nil && node.Metadata.DeletionTimestamp != "" {
		return true
	}

	if reconciler != nil && reconciler.AcceleratorManager != nil {
		return true
	}

	if node.Spec.Warm != nil && len(node.Spec.Warm.Images) > 0 {
		return true
	}

	return len(node.Spec.Components) > 0
}

func BuildStaticNodeStatus(
	node *v1.StaticNode,
	result *StaticNodeReconcileResult,
	reconcileErr error,
) v1.StaticNodeStatus {
	status := v1.StaticNodeStatus{}
	if node != nil && node.Status != nil {
		status = *node.Status
	}

	if result != nil {
		status.Accelerator = result.Accelerator
		status.Warm = result.Warm
		status.Components = result.Components
	}

	if reconcileErr != nil {
		status.Phase = v1.StaticNodePhaseFailed
		status.ErrorMessage = reconcileErr.Error()

		return status
	}

	status.ErrorMessage = ""

	if status.Warm != nil && !status.Warm.Ready {
		status.Phase = v1.StaticNodePhaseWarming

		return status
	}

	if !allNodeComponentsReady(status.Components) {
		status.Phase = v1.StaticNodePhaseReconciling

		return status
	}

	status.Phase = v1.StaticNodePhaseReady
	status.LastTransitionTime = time.Now().UTC().Format(time.RFC3339)

	return status
}

func buildStaticNodeStatus(
	node *v1.StaticNode,
	result *StaticNodeReconcileResult,
	reconcileErr error,
) v1.StaticNodeStatus {
	return BuildStaticNodeStatus(node, result, reconcileErr)
}

func allNodeComponentsReady(components []v1.NodeComponentStatus) bool {
	for _, component := range components {
		if !component.Ready || component.Phase != v1.NodeComponentPhaseRunning {
			return false
		}
	}

	return true
}
