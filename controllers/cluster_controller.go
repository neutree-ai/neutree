package controllers

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"

	"github.com/neutree-ai/neutree/internal/orchestrator"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ClusterController struct {
	baseController *BaseController

	storage               storage.Storage
	imageService          registry.ImageService
	defaultClusterVersion string

	syncHandler func(cluster *v1.Cluster) error

	// metrics
	MetricsRemoteWriteURL  string
	LocalMetricsConfigPath string
}

type ClusterControllerOption struct {
	ImageService          registry.ImageService
	Storage               storage.Storage
	Workers               int
	DefaultClusterVersion string

	// metrics option
	MetricsRemoteWriteURL  string
	LocalMetricsConfigPath string
}

func NewClusterController(opt *ClusterControllerOption) (*ClusterController, error) {
	c := &ClusterController{
		baseController: &BaseController{
			queue: workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(),
				workqueue.RateLimitingQueueConfig{Name: "cluster"}),
			workers:      opt.Workers,
			syncInterval: time.Second * 10,
		},
		storage:               opt.Storage,
		imageService:          opt.ImageService,
		defaultClusterVersion: opt.DefaultClusterVersion,

		MetricsRemoteWriteURL:  opt.MetricsRemoteWriteURL,
		LocalMetricsConfigPath: opt.LocalMetricsConfigPath,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *ClusterController) Start(ctx context.Context) {
	klog.Infof("Starting cluster controller")
	c.baseController.Start(ctx, c, c)
}

func (c *ClusterController) ListKeys() ([]interface{}, error) {
	clusters, err := c.storage.ListCluster(storage.ListOption{})
	if err != nil {
		return nil, err
	}

	keys := make([]interface{}, len(clusters))
	for i := range clusters {
		keys[i] = clusters[i].ID
	}

	return keys, nil
}

func (c *ClusterController) Reconcile(key interface{}) error {
	clusterID, ok := key.(int)
	if !ok {
		return errors.New("failed to assert key to clusterID")
	}

	obj, err := c.storage.GetCluster(strconv.Itoa(clusterID))
	if err != nil {
		return errors.Wrapf(err, "failed to get cluster %s", strconv.Itoa(clusterID))
	}

	klog.V(4).Info("Reconciling cluster " + obj.Metadata.Name)

	return c.syncHandler(obj)
}

func (c *ClusterController) sync(obj *v1.Cluster) error {
	var (
		err error
	)

	// set default cluster version
	if obj.Spec.Version == "" {
		obj.Spec.Version = c.defaultClusterVersion
	}

	imageRegistry, err := c.getRelateImageRegistry(obj)
	if err != nil {
		return errors.Wrap(err, "failed to get relate image registry")
	}

	clusterOrchestrator, err := orchestrator.NewOrchestrator(orchestrator.Options{
		Cluster:                obj,
		ImageRegistry:          imageRegistry,
		ImageService:           c.imageService,
		MetricsRemoteWriteURL:  c.MetricsRemoteWriteURL,
		LocalCollectConfigPath: c.LocalMetricsConfigPath,
	})
	if err != nil {
		return err
	}

	if obj.Metadata.DeletionTimestamp != "" {
		return c.reconcileDelete(obj, clusterOrchestrator)
	}

	return c.reconcileNormal(obj, clusterOrchestrator)
}

func (c *ClusterController) reconcileNormal(cluster *v1.Cluster, clusterOrchestrator orchestrator.Orchestrator) error {
	var (
		err    error
		headIP string
		phase  v1.ClusterPhase
	)

	defer func() {
		phase = v1.ClusterPhaseRunning
		if err != nil {
			phase = v1.ClusterPhaseFailed
		}

		updateStatusErr := c.updateStatus(cluster, clusterOrchestrator, phase, err)
		if updateStatusErr != nil {
			klog.Errorf("failed to update cluster %s status, err: %v", cluster.Metadata.Name, updateStatusErr)
		}
	}()

	if !cluster.IsInitialized() {
		klog.Info("Initializing cluster " + cluster.Metadata.Name)

		headIP, err = clusterOrchestrator.CreateCluster()
		if err != nil {
			return errors.Wrap(err, "failed to create cluster "+cluster.Metadata.Name)
		}

		err = clusterOrchestrator.HealthCheck()
		if err != nil {
			klog.Info("waiting for cluster " + cluster.Metadata.Name + " initializing")
			return errors.Wrap(err, "failed to health check cluster "+cluster.Metadata.Name)
		}

		cluster.Status = &v1.ClusterStatus{
			DashboardURL: fmt.Sprintf("http://%s:8265", headIP),
			Initialized:  true,
		}

		return nil
	}

	err = clusterOrchestrator.SyncCluster()
	if err != nil {
		return errors.Wrap(err, "sync cluster failed")
	}

	err = clusterOrchestrator.HealthCheck()
	if err != nil {
		return errors.Wrap(err, "health check cluster failed")
	}

	return nil
}

func (c *ClusterController) reconcileDelete(cluster *v1.Cluster, clusterOrchestrator orchestrator.Orchestrator) error {
	if cluster.Status != nil && cluster.Status.Phase == v1.ClusterPhaseDeleted {
		err := c.storage.DeleteCluster(strconv.Itoa(cluster.ID))
		if err != nil {
			return errors.Wrap(err, "failed to delete cluster "+cluster.Metadata.Name)
		}

		return nil
	}

	klog.Info("Deleting cluster " + cluster.Metadata.Name)

	err := clusterOrchestrator.DeleteCluster()
	if err != nil {
		return errors.Wrap(err, "failed to delete ray cluster "+cluster.Metadata.Name)
	}

	err = c.updateStatus(cluster, clusterOrchestrator, v1.ClusterPhaseDeleted, nil)
	if err != nil {
		klog.Errorf("failed to update cluster %s status, err: %v", cluster.Metadata.Name, err)
	}

	return nil
}

func (c *ClusterController) getRelateImageRegistry(cluster *v1.Cluster) (*v1.ImageRegistry, error) {
	imageRegistryFilter := []storage.Filter{
		{
			Column:   "metadata->name",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Spec.ImageRegistry),
		},
	}

	if cluster.Metadata.Workspace != "" {
		imageRegistryFilter = append(imageRegistryFilter, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Metadata.Workspace),
		})
	}

	imageRegistryList, err := c.storage.ListImageRegistry(storage.ListOption{Filters: imageRegistryFilter})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list image registry")
	}

	if len(imageRegistryList) == 0 {
		return nil, errors.New("relate image registry not found")
	}

	return &imageRegistryList[0], nil
}

func (c *ClusterController) updateStatus(obj *v1.Cluster, clusterOrchestrator orchestrator.Orchestrator, phase v1.ClusterPhase, err error) error {
	newStatus := &v1.ClusterStatus{
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
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
		newStatus.DesiredNodes = obj.Status.DesiredNodes
	}

	if obj.IsInitialized() && obj.Metadata.DeletionTimestamp == "" {
		clusterStatus, getClusterStatusErr := clusterOrchestrator.ClusterStatus()
		if getClusterStatusErr == nil {
			newStatus.ReadyNodes = clusterStatus.ReadyNodes
			newStatus.Version = clusterStatus.NeutreeServeVersion
			newStatus.RayVersion = clusterStatus.RayVersion
			newStatus.DesiredNodes = clusterStatus.DesireNodes
		}
	}

	if err != nil {
		newStatus.ErrorMessage = err.Error()
	}

	return c.storage.UpdateCluster(strconv.Itoa(obj.ID), &v1.Cluster{Status: newStatus})
}
