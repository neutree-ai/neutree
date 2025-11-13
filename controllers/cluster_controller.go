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
	cluster, ok := obj.(*v1.Cluster)
	if !ok {
		return errors.New("failed to assert obj to *v1.Cluster")
	}

	klog.V(4).Info("Reconciling cluster " + cluster.Metadata.WorkspaceName())

	return c.syncHandler(cluster)
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
	var (
		err   error
		phase v1.ClusterPhase
	)

	defer func() {
		phase = v1.ClusterPhaseRunning
		if err != nil {
			phase = v1.ClusterPhaseFailed
			if c.Status != nil && !c.Status.Initialized {
				phase = v1.ClusterPhaseInitializing
			}
		}

		updateStatusErr := controller.updateStatus(c, phase, err)
		if updateStatusErr != nil {
			klog.Errorf("failed to update cluster %s status, err: %v", c.Metadata.WorkspaceName(), updateStatusErr)
		}
	}()

	r, err := cluster.NewReconcile(c, controller.acceleratorManager, controller.storage, controller.metricsRemoteWriteURL)
	if err != nil {
		return errors.Wrapf(err, "failed to create cluster reconciler for cluster %s", c.Metadata.WorkspaceName())
	}

	err = r.Reconcile(context.Background(), c)
	if err != nil {
		return errors.Wrapf(err, "failed to reconcile cluster %s", c.Metadata.WorkspaceName())
	}

	klog.V(4).Info("Cluster " + c.Metadata.WorkspaceName() + " reconcile succeeded, syncing to gateway")

	err = controller.gw.SyncCluster(c)
	if err != nil {
		return errors.Wrapf(err, "failed to sync cluster %s to gateway", c.Metadata.WorkspaceName())
	}

	if c.Spec.Type == v1.SSHClusterType {
		controller.obsCollectConfigManager.GetMetricsCollectConfigManager().RegisterMetricsMonitor(c.Key(), monitoring.NewClusterMonitor(c))
	}

	return nil
}

func (controller *ClusterController) reconcileDelete(c *v1.Cluster) error {
	if c.Status != nil && c.Status.Phase == v1.ClusterPhaseDeleted {
		klog.Info("Cluster " + c.Metadata.WorkspaceName() + " already deleted, delete resource from storage")

		err := controller.storage.DeleteCluster(strconv.Itoa(c.ID))
		if err != nil {
			return errors.Wrap(err, "failed to delete cluster "+c.Metadata.WorkspaceName())
		}

		return nil
	}

	klog.Info("Deleting cluster " + c.Metadata.WorkspaceName())

	err := controller.gw.DeleteCluster(c)
	if err != nil {
		return errors.Wrap(err, "failed to delete cluster backend service "+c.Metadata.WorkspaceName())
	}

	if c.Spec.Type == v1.SSHClusterType {
		controller.obsCollectConfigManager.GetMetricsCollectConfigManager().UnregisterMetricsMonitor(c.Key())
	}

	r, err := cluster.NewReconcile(c, controller.acceleratorManager, controller.storage, controller.metricsRemoteWriteURL)
	if err != nil {
		return errors.Wrapf(err, "failed to create cluster reconciler for cluster %s", c.Metadata.WorkspaceName())
	}

	err = r.ReconcileDelete(context.Background(), c)
	if err != nil {
		return errors.Wrapf(err, "failed to reconcile delete cluster %s", c.Metadata.WorkspaceName())
	}

	klog.Info("Cluster " + c.Metadata.WorkspaceName() + " delete finished, mark as deleted")

	err = controller.updateStatus(c, v1.ClusterPhaseDeleted, nil)
	if err != nil {
		klog.Errorf("failed to update cluster %s status, err: %v", c.Metadata.WorkspaceName(), err)
	}

	return nil
}

func (c *ClusterController) updateStatus(obj *v1.Cluster, phase v1.ClusterPhase, err error) error {
	newStatus := &v1.ClusterStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
		ErrorMessage:       FormatErrorForStatus(err),
	}

	if obj.Status != nil {
		newStatus.Initialized = obj.Status.Initialized
		newStatus.DashboardURL = obj.Status.DashboardURL
		newStatus.NodeProvisionStatus = obj.Status.NodeProvisionStatus
		newStatus.ReadyNodes = obj.Status.ReadyNodes
		newStatus.DesiredNodes = obj.Status.DesiredNodes
		newStatus.Version = obj.Status.Version
		newStatus.RayVersion = obj.Status.RayVersion
		// Preserve existing ResourceInfo - it will be updated by cluster reconcilers
		newStatus.ResourceInfo = obj.Status.ResourceInfo
	}

	if err != nil {
		newStatus.ErrorMessage = err.Error()
	}

	return c.storage.UpdateCluster(strconv.Itoa(obj.ID), &v1.Cluster{Status: newStatus})
}
