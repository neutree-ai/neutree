package cluster

import (
	"strconv"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// WriteEarlyUpdating writes Updating phase to storage if spec hash changed.
// This provides immediate user feedback that an update is in progress.
// This is best-effort: failures are logged but do not block reconciliation.
func WriteEarlyUpdating(cluster *v1.Cluster, s storage.Storage) {
	if cluster.Status == nil || cluster.Status.ObservedSpecHash == "" {
		return
	}

	currentHash := ComputeClusterSpecHash(cluster.Spec)
	if cluster.Status.ObservedSpecHash != currentHash && cluster.Status.Phase != v1.ClusterPhaseUpdating {
		cluster.Status.Phase = v1.ClusterPhaseUpdating
		if err := s.UpdateCluster(strconv.Itoa(cluster.ID), &v1.Cluster{Status: cluster.Status}); err != nil {
			klog.Warningf("failed to write early Updating status for cluster %s: %v", cluster.Metadata.WorkspaceName(), err)
		}
	}
}

// WriteEarlyDeleting writes Deleting phase to storage if not already Deleting.
// This provides immediate user feedback that deletion is in progress.
// This is best-effort: failures are logged but do not block reconciliation.
func WriteEarlyDeleting(cluster *v1.Cluster, s storage.Storage) {
	if cluster.Status == nil || cluster.Status.Phase == v1.ClusterPhaseDeleting {
		return
	}

	cluster.Status.Phase = v1.ClusterPhaseDeleting
	if err := s.UpdateCluster(strconv.Itoa(cluster.ID), &v1.Cluster{Status: cluster.Status}); err != nil {
		klog.Warningf("failed to write early Deleting status for cluster %s: %v", cluster.Metadata.WorkspaceName(), err)
	}
}
