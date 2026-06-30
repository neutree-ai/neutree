package cluster

import (
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

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

	if len(status.Components) == 0 {
		status.Phase = v1.StaticNodePhaseReconciling

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
