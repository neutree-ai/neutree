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

	// Backfill status.version for legacy clusters that were created before
	// version tracking was introduced. Without this, changing spec.version
	// on a legacy cluster would show as Updating instead of Upgrading.
	// We set it to the current spec.version so the cluster starts in a
	// consistent state; the next spec.version change will trigger Upgrading.
	if obj.IsInitialized() && obj.Status != nil && obj.Status.Version == "" {
		obj.Status.Version = obj.Spec.Version
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
	isForceDelete := v1.IsForceDelete(c.Metadata.Annotations)

	if c.Status != nil && c.Status.Phase == v1.ClusterPhaseDeleted {
		klog.Info("Cluster " + c.Metadata.WorkspaceName() + " already deleted, delete resource from storage")

		err := controller.storage.DeleteCluster(strconv.Itoa(c.ID))
		if err != nil {
			return errors.Wrap(err, "failed to delete cluster "+c.Metadata.WorkspaceName())
		}

		return nil
	}

	klog.Infof("Deleting cluster %s (force=%v)", c.Metadata.WorkspaceName(), isForceDelete)

	var reconcileErr error

	defer func() {
		controller.updateClusterStatus(c, reconcileErr)
	}()

	reconcileErr = func() error {
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

	if reconcileErr != nil && !isForceDelete {
		return reconcileErr
	}

	LogForceDeletionWarning(isForceDelete, "cluster", c.Metadata.Workspace, c.Metadata.Name, reconcileErr)

	return nil
}

// updateClusterStatus determines cluster phase and updates storage.
// reconcileErr == nil means resources are ready (Reconcile includes status checks).
func (controller *ClusterController) updateClusterStatus(c *v1.Cluster, reconcileErr error) {
	if c.Metadata.DeletionTimestamp != "" {
		phase := cluster.DetermineClusterDeletePhase(reconcileErr == nil, c)

		if updateErr := controller.updateStatus(c, phase, reconcileErr); updateErr != nil {
			klog.Errorf("failed to update cluster %s status, err: %v", c.Metadata.WorkspaceName(), updateErr)
		}

		return
	}

	phase := cluster.DetermineClusterPhase(reconcileErr == nil, c)

	// Set ObservedSpecHash when Running
	if phase == v1.ClusterPhaseRunning {
		if c.Status == nil {
			c.Status = &v1.ClusterStatus{}
		}

		c.Status.ObservedSpecHash = cluster.ComputeClusterSpecHash(c.Spec)
	}

	if updateErr := controller.updateStatus(c, phase, reconcileErr); updateErr != nil {
		klog.Errorf("failed to update cluster %s status, err: %v", c.Metadata.WorkspaceName(), updateErr)
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
	}

	if err != nil {
		newStatus.ErrorMessage = err.Error()
	}

	return controller.storage.UpdateCluster(strconv.Itoa(obj.ID), &v1.Cluster{Status: newStatus})
}
