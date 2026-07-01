package cluster

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// DetermineClusterPhase determines the normal cluster phase based on resource readiness and cluster context.
// Intended for use by controllers and other components that need to derive a cluster's phase:
//
//	isResourceReady -> Running
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

	if needsVersionUpgrade(cluster) {
		return v1.ClusterPhaseUpgrading
	}

	if cluster.Status != nil && cluster.Status.ObservedSpecHash != "" {
		currentHash := ComputeClusterSpecHash(cluster.Spec)
		if cluster.Status.ObservedSpecHash != currentHash {
			return v1.ClusterPhaseUpdating
		}
	}

	return v1.ClusterPhaseFailed
}

func DetermineStaticNodeClusterBackedClusterPhase(
	isResourceReady bool,
	cluster *v1.Cluster,
	staticStatus *v1.StaticNodeClusterStatus,
	specObserved bool,
) v1.ClusterPhase {
	if isResourceReady {
		return DetermineClusterPhase(true, cluster)
	}

	if cluster == nil || !cluster.IsInitialized() {
		return v1.ClusterPhaseInitializing
	}

	if staticStatus != nil {
		switch staticStatus.Phase {
		case v1.StaticNodeClusterPhaseFailed, v1.StaticNodeClusterPhaseDegraded:
			return v1.ClusterPhaseFailed
		case v1.StaticNodeClusterPhaseUpgrading:
			return v1.ClusterPhaseUpgrading
		case v1.StaticNodeClusterPhaseProvisioning:
			if staticNodeClusterStatusNeedsUpgrade(cluster, staticStatus) {
				return v1.ClusterPhaseUpgrading
			}

			return v1.ClusterPhaseUpdating
		case v1.StaticNodeClusterPhaseReady:
			if staticNodeClusterStatusNeedsUpgrade(cluster, staticStatus) {
				return v1.ClusterPhaseUpgrading
			}

			return v1.ClusterPhaseUpdating
		}
	}

	if staticNodeClusterStatusNeedsUpgrade(cluster, staticStatus) {
		return v1.ClusterPhaseUpgrading
	}

	if !specObserved {
		return v1.ClusterPhaseUpdating
	}

	return DetermineClusterPhase(false, cluster)
}

func staticNodeClusterStatusNeedsUpgrade(cluster *v1.Cluster, status *v1.StaticNodeClusterStatus) bool {
	if cluster == nil {
		return false
	}

	desiredVersion := cluster.GetVersion()
	if desiredVersion == "" {
		return false
	}

	if cluster.Status != nil && cluster.Status.Version != "" && cluster.Status.Version != desiredVersion {
		return true
	}

	return status != nil && status.Version != "" && status.Version != desiredVersion
}

// needsVersionUpgrade returns true when the cluster's actual version differs
// from the desired version, indicating a version upgrade is needed.
// Used by both phase determination and SSH reconcile logic.
func needsVersionUpgrade(cluster *v1.Cluster) bool {
	return cluster.Status != nil && cluster.Status.Version != "" &&
		cluster.Spec != nil && cluster.Spec.Version != "" &&
		cluster.Status.Version != cluster.Spec.Version
}

// DetermineClusterDeletePhase determines the deletion phase.
//
//	isDeleteCompleted || force-delete -> Deleted
//	else -> Deleting
func DetermineClusterDeletePhase(isDeleteCompleted bool, cluster *v1.Cluster) v1.ClusterPhase {
	if isDeleteCompleted || v1.IsForceDelete(cluster.GetAnnotations()) {
		return v1.ClusterPhaseDeleted
	}

	return v1.ClusterPhaseDeleting
}
