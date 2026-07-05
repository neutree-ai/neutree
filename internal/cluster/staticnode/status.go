package staticnode

import (
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func BuildStatus(
	node *v1.StaticNode,
	result *ReconcileResult,
	reconcileErr error,
) v1.StaticNodeStatus {
	status := v1.StaticNodeStatus{}
	if node != nil && node.Status != nil {
		status = *node.Status
	}

	if result != nil {
		if result.Accelerator != nil {
			status.Accelerator = result.Accelerator
		}

		if result.Allocations != nil {
			status.Allocations = result.Allocations
		}

		if result.Warm != nil {
			status.Warm = result.Warm
		}

		if result.Components != nil {
			status.Components = result.Components
		}
	}

	if reconcileErr != nil {
		setPhase(&status, v1.StaticNodePhaseFailed)
		status.ErrorMessage = reconcileErr.Error()

		return status
	}

	status.ErrorMessage = ""

	if status.Warm != nil && !status.Warm.Ready {
		setPhase(&status, v1.StaticNodePhaseWarming)

		return status
	}

	if len(status.Components) == 0 {
		setPhase(&status, v1.StaticNodePhaseReconciling)

		return status
	}

	if !allNodeComponentsReady(status.Components) {
		setPhase(&status, v1.StaticNodePhaseReconciling)

		return status
	}

	setPhase(&status, v1.StaticNodePhaseReady)

	return status
}

func allNodeComponentsReady(components []v1.NodeComponentStatus) bool {
	for _, component := range components {
		if !component.Ready || component.Phase != v1.NodeComponentPhaseRunning {
			return false
		}
	}

	return true
}

func setPhase(status *v1.StaticNodeStatus, phase v1.StaticNodePhase) {
	if status.Phase != phase || status.LastTransitionTime == "" {
		status.LastTransitionTime = time.Now().UTC().Format(time.RFC3339)
	}

	status.Phase = phase
}
