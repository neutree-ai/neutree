package cluster

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// DetermineClusterPhase determines the cluster phase based on resource readiness and cluster context.
// Shared logic used by both SSH and K8s GetClusterStatus():
//
//	resource ready -> Running
//	!Initialized -> Initializing
//	ObservedSpecHash != "" && ObservedSpecHash != currentHash -> Updating
//	else -> Failed
func DetermineClusterPhase(isResourceReady bool, cluster *v1.Cluster) v1.ClusterPhase {
	if isResourceReady {
		return v1.ClusterPhaseRunning
	}

	if !cluster.IsInitialized() {
		return v1.ClusterPhaseInitializing
	}

	if cluster.Status != nil && cluster.Status.ObservedSpecHash != "" {
		currentHash := ComputeClusterSpecHash(cluster.Spec)
		if cluster.Status.ObservedSpecHash != currentHash {
			return v1.ClusterPhaseUpdating
		}
	}

	return v1.ClusterPhaseFailed
}
