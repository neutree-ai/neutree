package cluster

import (
	"strconv"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// WriteEarlyStatus writes Initializing or Updating phase to storage using DetermineClusterPhase.
// This provides immediate user feedback before reconciliation completes.
// This is best-effort: failures are logged but do not block reconciliation.
func WriteEarlyStatus(cluster *v1.Cluster, s storage.Storage) {
	phase := DetermineClusterPhase(false, cluster)

	if phase != v1.ClusterPhaseInitializing && phase != v1.ClusterPhaseUpdating {
		return
	}

	if cluster.Status != nil && cluster.Status.Phase == phase {
		return
	}

	if cluster.Status == nil {
		cluster.Status = &v1.ClusterStatus{}
	}

	cluster.Status.Phase = phase

	if err := s.UpdateCluster(strconv.Itoa(cluster.ID), &v1.Cluster{Status: cluster.Status}); err != nil {
		klog.Warningf("failed to write early %s status for cluster %s: %v", phase, cluster.Metadata.WorkspaceName(), err)
	}
}

// WriteEarlyDeleting writes Deleting phase to storage if not already Deleting.
// This provides immediate user feedback that deletion is in progress.
// This is best-effort: failures are logged but do not block reconciliation.
func WriteEarlyDeleting(cluster *v1.Cluster, s storage.Storage) {
	if cluster.Status != nil && cluster.Status.Phase == v1.ClusterPhaseDeleting {
		return
	}

	if cluster.Status == nil {
		cluster.Status = &v1.ClusterStatus{}
	}

	cluster.Status.Phase = v1.ClusterPhaseDeleting
	if err := s.UpdateCluster(strconv.Itoa(cluster.ID), &v1.Cluster{Status: cluster.Status}); err != nil {
		klog.Warningf("failed to write early Deleting status for cluster %s: %v", cluster.Metadata.WorkspaceName(), err)
	}
}
