package controllers

import (
	"context"
	"strconv"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	"github.com/neutree-ai/neutree/internal/observability/monitoring"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ClusterController struct {
	storage               storage.Storage
	defaultClusterVersion string

	syncHandler func(cluster *v1.Cluster) error

	obsCollectConfigManager manager.ObsCollectConfigManager

	metricsRemoteWriteURL string

	gw gateway.Gateway

	acceleratorManager accelerator.Manager
}

type ClusterControllerOption struct {
	Storage               storage.Storage
	DefaultClusterVersion string
	MetricsRemoteWriteURL string

	ObsCollectConfigManager manager.ObsCollectConfigManager
	Gw                      gateway.Gateway
	AcceleratorManager      accelerator.Manager
}

func NewClusterController(opt *ClusterControllerOption) (*ClusterController, error) {
	c := &ClusterController{
		storage:               opt.Storage,
		defaultClusterVersion: opt.DefaultClusterVersion,

		obsCollectConfigManager: opt.ObsCollectConfigManager,
		metricsRemoteWriteURL:   opt.MetricsRemoteWriteURL,

		gw:                 opt.Gw,
		acceleratorManager: opt.AcceleratorManager,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *ClusterController) Reconcile(obj interface{}) error {
	cl, ok := obj.(*v1.Cluster)
	if !ok {
		return errors.New("failed to assert obj to *v1.Cluster")
	}

	klog.V(4).Info("Reconciling cluster " + cl.Metadata.WorkspaceName())

	return c.syncHandler(cl)
}

func (controller *ClusterController) sync(obj *v1.Cluster) error {
	// set default cluster version
	if obj.Spec.Version == "" {
		obj.Spec.Version = controller.defaultClusterVersion
	}

	if obj.Metadata.DeletionTimestamp != "" {
		return controller.reconcileDelete(obj)
	}

	return controller.reconcileNormal(obj)
}

func (controller *ClusterController) reconcileNormal(c *v1.Cluster) error {
	var reconcileErr error

	defer func() {
		controller.updateClusterStatus(c, reconcileErr)
	}()

	r, err := cluster.NewReconcile(c, controller.acceleratorManager, controller.storage, controller.metricsRemoteWriteURL)
	if err != nil {
		reconcileErr = errors.Wrapf(err, "failed to create cluster reconciler for cluster %s", c.Metadata.WorkspaceName())
		return reconcileErr
	}

	reconcileErr = r.Reconcile(context.Background(), c)
	if reconcileErr != nil {
		reconcileErr = errors.Wrapf(reconcileErr, "failed to reconcile cluster %s", c.Metadata.WorkspaceName())
		return reconcileErr
	}

	klog.V(4).Info("Cluster " + c.Metadata.WorkspaceName() + " reconcile succeeded, syncing to gateway")

	reconcileErr = controller.gw.SyncCluster(c)
	if reconcileErr != nil {
		reconcileErr = errors.Wrapf(reconcileErr, "failed to sync cluster %s to gateway", c.Metadata.WorkspaceName())
		return reconcileErr
	}

	if c.Spec.Type == v1.SSHClusterType {
		controller.obsCollectConfigManager.GetMetricsCollectConfigManager().RegisterMetricsMonitor(c.Key(), monitoring.NewClusterMonitor(c))
	}

	return nil
}

func (controller *ClusterController) reconcileDelete(c *v1.Cluster) error {
	isForceDelete := IsForceDelete(c.Metadata.Annotations)

	if c.Status != nil && c.Status.Phase == v1.ClusterPhaseDeleted {
		klog.Info("Cluster " + c.Metadata.WorkspaceName() + " already deleted, delete resource from storage")

		err := controller.storage.DeleteCluster(strconv.Itoa(c.ID))
		if err != nil {
			return errors.Wrap(err, "failed to delete cluster "+c.Metadata.WorkspaceName())
		}

		return nil
	}

	klog.Infof("Deleting cluster %s (force=%v)", c.Metadata.WorkspaceName(), isForceDelete)

	deleteErr := func() error {
		if err := controller.gw.DeleteCluster(c); err != nil {
			return errors.Wrap(err, "failed to delete cluster backend service "+c.Metadata.WorkspaceName())
		}

		if c.Spec.Type == v1.SSHClusterType {
			controller.obsCollectConfigManager.GetMetricsCollectConfigManager().UnregisterMetricsMonitor(c.Key())
		}

		r, err := cluster.NewReconcile(c, controller.acceleratorManager, controller.storage, controller.metricsRemoteWriteURL)
		if err != nil {
			return errors.Wrapf(err, "failed to create cluster reconciler for cluster %s", c.Metadata.WorkspaceName())
		}

		if err = r.ReconcileDelete(context.Background(), c); err != nil {
			return errors.Wrapf(err, "failed to reconcile delete cluster %s", c.Metadata.WorkspaceName())
		}

		return nil
	}()

	// For non-force delete failure, update status with Deleting + error message
	if deleteErr != nil && !isForceDelete {
		updateErr := controller.updateStatus(c, v1.ClusterPhaseDeleting, deleteErr)
		if updateErr != nil {
			klog.Errorf("failed to update cluster %s status, err: %v", c.Metadata.WorkspaceName(), updateErr)
		}

		return deleteErr
	}

	// Update status for successful delete or force delete
	updateErr := controller.updateStatus(c, v1.ClusterPhaseDeleted, deleteErr)
	if updateErr != nil {
		klog.Errorf("failed to update cluster %s status, err: %v", c.Metadata.WorkspaceName(), updateErr)
		return errors.Wrapf(updateErr, "failed to update cluster %s status", c.Metadata.WorkspaceName())
	}

	LogForceDeletionWarning(isForceDelete, "cluster", c.Metadata.Workspace, c.Metadata.Name, deleteErr)

	return nil
}

// updateClusterStatus gets the actual cluster status from the reconciler and updates storage.
// actualStatus has the highest priority. Falls back to reconcileErr only when GetClusterStatus fails.
func (controller *ClusterController) updateClusterStatus(c *v1.Cluster, reconcileErr error) {
	r, err := cluster.NewReconcile(c, controller.acceleratorManager, controller.storage, controller.metricsRemoteWriteURL)
	if err != nil {
		klog.Errorf("failed to create reconciler for status check of cluster %s: %v", c.Metadata.WorkspaceName(), err)

		if reconcileErr != nil {
			phase := v1.ClusterPhaseFailed
			if !c.IsInitialized() {
				phase = v1.ClusterPhaseInitializing
			}

			_ = controller.updateStatus(c, phase, reconcileErr)
		}

		return
	}

	// GetClusterStatus is always called regardless of reconcileErr
	actualStatus, statusErr := r.GetClusterStatus(context.Background(), c)

	if statusErr == nil && actualStatus != nil {
		// Trust actualStatus completely
		mergeStatus(c, actualStatus)

		updateErr := controller.updateStatus(c, actualStatus.Phase, nil)
		if updateErr != nil {
			klog.Errorf("failed to update cluster %s status, err: %v", c.Metadata.WorkspaceName(), updateErr)
		}

		return
	}

	// Fallback: GetClusterStatus failed
	klog.Warningf("failed to get cluster status for %s: %v", c.Metadata.WorkspaceName(), statusErr)

	if reconcileErr != nil {
		phase := v1.ClusterPhaseFailed
		if !c.IsInitialized() {
			phase = v1.ClusterPhaseInitializing
		}

		updateErr := controller.updateStatus(c, phase, reconcileErr)
		if updateErr != nil {
			klog.Errorf("failed to update cluster %s status, err: %v", c.Metadata.WorkspaceName(), updateErr)
		}

		return
	}

	// No reconcile error but GetClusterStatus failed
	if !c.IsInitialized() {
		// During initialization, report statusErr so user can see what's blocking
		updateErr := controller.updateStatus(c, v1.ClusterPhaseInitializing, statusErr)
		if updateErr != nil {
			klog.Errorf("failed to update cluster %s status, err: %v", c.Metadata.WorkspaceName(), updateErr)
		}

		return
	}

}

func (controller *ClusterController) updateStatus(obj *v1.Cluster, phase v1.ClusterPhase, err error) error {
	newStatus := &v1.ClusterStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
	}

	if obj.Status != nil {
		newStatus.Initialized = obj.Status.Initialized
		newStatus.DashboardURL = obj.Status.DashboardURL
		newStatus.NodeProvisionStatus = obj.Status.NodeProvisionStatus
		newStatus.ReadyNodes = obj.Status.ReadyNodes
		newStatus.DesiredNodes = obj.Status.DesiredNodes
		newStatus.Version = obj.Status.Version
		newStatus.RayVersion = obj.Status.RayVersion
		newStatus.ResourceInfo = obj.Status.ResourceInfo
		newStatus.AcceleratorType = obj.Status.AcceleratorType
		newStatus.ObservedSpecHash = obj.Status.ObservedSpecHash
		newStatus.ErrorMessage = obj.Status.ErrorMessage
	}

	if err != nil {
		newStatus.ErrorMessage = err.Error()
	}

	return controller.storage.UpdateCluster(strconv.Itoa(obj.ID), &v1.Cluster{Status: newStatus})
}

// mergeStatus merges actualStatus fields into the cluster's in-memory status.
// Controller then calls updateStatus which reads from cluster.Status to persist.
func mergeStatus(c *v1.Cluster, actualStatus *v1.ClusterStatus) {
	if actualStatus == nil {
		return
	}

	if c.Status == nil {
		c.Status = &v1.ClusterStatus{}
	}

	c.Status.Phase = actualStatus.Phase
	c.Status.ReadyNodes = actualStatus.ReadyNodes
	c.Status.DesiredNodes = actualStatus.DesiredNodes
	c.Status.Version = actualStatus.Version
	c.Status.RayVersion = actualStatus.RayVersion
	c.Status.ResourceInfo = actualStatus.ResourceInfo
	c.Status.NodeProvisionStatus = actualStatus.NodeProvisionStatus
	c.Status.Initialized = actualStatus.Initialized
	c.Status.AcceleratorType = actualStatus.AcceleratorType
	c.Status.ErrorMessage = actualStatus.ErrorMessage
	c.Status.ObservedSpecHash = actualStatus.ObservedSpecHash

	// Smart merge: preserve existing DashboardURL if not set
	if actualStatus.DashboardURL != "" {
		c.Status.DashboardURL = actualStatus.DashboardURL
	}
}
