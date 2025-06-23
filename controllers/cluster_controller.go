package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"

	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	"github.com/neutree-ai/neutree/internal/observability/monitoring"
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

	obsCollectConfigManager manager.ObsCollectConfigManager

	metricsRemoteWriteURL string

	gw gateway.Gateway

	acceleratorManager *accelerator.Manager
}

type ClusterControllerOption struct {
	ImageService          registry.ImageService
	Storage               storage.Storage
	Workers               int
	DefaultClusterVersion string
	MetricsRemoteWriteURL string

	ObsCollectConfigManager manager.ObsCollectConfigManager
	Gw                      gateway.Gateway
	AcceleratorManager      *accelerator.Manager
}

func NewClusterController(opt *ClusterControllerOption) (*ClusterController, error) {
	c := &ClusterController{
		baseController: &BaseController{
			//nolint:staticcheck
			queue: workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(),
				workqueue.RateLimitingQueueConfig{Name: "cluster"}),
			workers:      opt.Workers,
			syncInterval: time.Second * 10,
		},
		storage:               opt.Storage,
		imageService:          opt.ImageService,
		defaultClusterVersion: opt.DefaultClusterVersion,

		obsCollectConfigManager: opt.ObsCollectConfigManager,
		metricsRemoteWriteURL:   opt.MetricsRemoteWriteURL,

		gw:                 opt.Gw,
		acceleratorManager: opt.AcceleratorManager,
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
	// set default cluster version
	if obj.Spec.Version == "" {
		obj.Spec.Version = c.defaultClusterVersion
	}

	if obj.Metadata.DeletionTimestamp != "" {
		return c.reconcileDelete(obj)
	}

	return c.reconcileNormal(obj)
}

func (c *ClusterController) reconcileNormal(cluster *v1.Cluster) error {
	var (
		err    error
		headIP string
		phase  v1.ClusterPhase
	)

	clusterOrchestrator, err := orchestrator.NewOrchestrator(orchestrator.Options{
		Cluster:               cluster,
		ImageService:          c.imageService,
		AcceleratorManager:    c.acceleratorManager,
		Storage:               c.storage,
		MetricsRemoteWriteURL: c.metricsRemoteWriteURL,
	})
	if err != nil {
		return err
	}

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

		cluster.Status = &v1.ClusterStatus{
			DashboardURL: fmt.Sprintf("http://%s:8265", headIP),
			Initialized:  true,
		}

		err = clusterOrchestrator.HealthCheck()
		if err != nil {
			klog.Info("waiting for cluster " + cluster.Metadata.Name + " healthy")
			return errors.Wrap(err, "failed to health check cluster "+cluster.Metadata.Name)
		}

		return nil
	}

	// only reconcile node when cluster is running.
	if cluster.Status != nil && cluster.Status.Phase == v1.ClusterPhaseRunning {
		err = c.reconcileNodes(cluster, clusterOrchestrator)
		if err != nil {
			return errors.Wrap(err, "failed to reconcile nodes")
		}
	}

	err = clusterOrchestrator.SyncCluster()
	if err != nil {
		return errors.Wrap(err, "sync cluster failed")
	}

	err = clusterOrchestrator.HealthCheck()
	if err != nil {
		return errors.Wrap(err, "health check cluster failed")
	}

	err = c.gw.SyncCluster(cluster)
	if err != nil {
		return errors.Wrap(err, "sync cluster backend service failed")
	}

	// ssh cluster use local metrics collector.
	if cluster.Spec.Type == "ssh" {
		c.obsCollectConfigManager.GetMetricsCollectConfigManager().RegisterMetricsMonitor(cluster.Key(), monitoring.NewClusterMonitor(cluster, clusterOrchestrator))
	}

	return nil
}

// reconcileNodes will reconcile the desired node of the cluster.
func (c *ClusterController) reconcileNodes(cluster *v1.Cluster, clusterOrchestrator orchestrator.Orchestrator) error {
	// only ssh should reconcile static node.
	if cluster.Spec.Type == "ssh" {
		err := c.reconcileStaticNodes(cluster, clusterOrchestrator)
		if err != nil {
			return errors.Wrap(err, "failed to reconcile cluster static node "+cluster.Metadata.Name)
		}
	}

	return nil
}

// 1. get desired static node ip from cluster spec
// 2. get static node provision status from cluster status
// 3. compare desired static node ip and static node provision status
// 4. start static node that not provisioned
// 5. stop static node that not in desired static node ip
func (c *ClusterController) reconcileStaticNodes(cluster *v1.Cluster, clusterOrchestrator orchestrator.Orchestrator) error { //nolint:funlen
	klog.V(4).Info("Reconciling Static Nodes for cluster " + cluster.Metadata.Name)

	var (
		desiredStaticNodeIpMap       = map[string]string{}
		staticNodeProvisionStatusMap = map[string]string{}
		nodeIpToStart                []string
		nodeIpToStop                 []string
	)

	// get desired static provision node ip from cluster spec
	desiredStaticWorkersIP := clusterOrchestrator.GetDesireStaticWorkersIP()

	for _, nodeIp := range desiredStaticWorkersIP {
		desiredStaticNodeIpMap[nodeIp] = nodeIp
	}

	// get static node provision status from cluster status
	if cluster.Status != nil && cluster.Status.NodeProvisionStatus != "" {
		err := json.Unmarshal([]byte(cluster.Status.NodeProvisionStatus), &staticNodeProvisionStatusMap)
		if err != nil {
			return errors.Wrap(err, "failed to unmarshal static node provision status")
		}
	}

	for nodeIp := range staticNodeProvisionStatusMap {
		if _, ok := desiredStaticNodeIpMap[nodeIp]; ok {
			continue
		}

		nodeIpToStop = append(nodeIpToStop, nodeIp)
	}

	for _, nodeIp := range desiredStaticNodeIpMap {
		// already provisioned, skip
		if _, ok := staticNodeProvisionStatusMap[nodeIp]; ok && staticNodeProvisionStatusMap[nodeIp] == v1.ProvisionedNodeProvisionStatus {
			continue
		}

		nodeIpToStart = append(nodeIpToStart, nodeIp)
	}

	nodeOpErrors := make([]error, len(nodeIpToStart)+len(nodeIpToStop))
	eg := &errgroup.Group{}

	for i := range nodeIpToStart {
		ip := nodeIpToStart[i]

		eg.Go(func() error {
			klog.Info("Starting ray node " + ip)

			err := clusterOrchestrator.StartNode(ip)
			if err != nil {
				nodeOpErrors[i] = errors.Wrap(err, "failed to start ray node "+ip)
			}

			return nil
		})
	}

	for i := range nodeIpToStop {
		ip := nodeIpToStop[i]

		eg.Go(func() error {
			klog.Info("Stopping ray node " + ip)

			err := clusterOrchestrator.StopNode(ip)
			if err != nil {
				nodeOpErrors[i+len(nodeIpToStart)] = errors.Wrap(err, "failed to stop ray node "+ip)
			}

			return nil
		})
	}

	eg.Wait() //nolint:errcheck

	// update static node provision status
	for i := range nodeIpToStart {
		if nodeOpErrors[i] == nil {
			staticNodeProvisionStatusMap[nodeIpToStart[i]] = v1.ProvisionedNodeProvisionStatus
		} else {
			staticNodeProvisionStatusMap[nodeIpToStart[i]] = v1.ProvisioningNodeProvisionStatus
		}
	}

	for i := range nodeIpToStop {
		if nodeOpErrors[len(nodeIpToStart)+i] == nil {
			delete(staticNodeProvisionStatusMap, nodeIpToStop[i])
		}
	}

	// update cluster labels
	staticNodeProvisionStatusContent, err := json.Marshal(staticNodeProvisionStatusMap)
	if err != nil {
		return errors.Wrap(err, "failed to marshal static node provision status")
	}

	if cluster.Status == nil {
		cluster.Status = &v1.ClusterStatus{}
	}

	cluster.Status.NodeProvisionStatus = string(staticNodeProvisionStatusContent)

	aggregateError := apierrors.NewAggregate(nodeOpErrors)
	if aggregateError != nil {
		return aggregateError
	}

	return nil
}

func (c *ClusterController) reconcileDelete(cluster *v1.Cluster) error {
	if cluster.Status != nil && cluster.Status.Phase == v1.ClusterPhaseDeleted {
		err := c.storage.DeleteCluster(strconv.Itoa(cluster.ID))
		if err != nil {
			return errors.Wrap(err, "failed to delete cluster "+cluster.Metadata.Name)
		}

		return nil
	}

	klog.Info("Deleting cluster " + cluster.Metadata.Name)

	c.obsCollectConfigManager.GetMetricsCollectConfigManager().UnregisterMetricsMonitor(cluster.Key())

	if cluster.IsInitialized() {
		clusterOrchestrator, err := orchestrator.NewOrchestrator(orchestrator.Options{
			Cluster:               cluster,
			ImageService:          c.imageService,
			Storage:               c.storage,
			AcceleratorManager:    c.acceleratorManager,
			MetricsRemoteWriteURL: c.metricsRemoteWriteURL,
		})
		if err != nil {
			return err
		}

		err = clusterOrchestrator.DeleteCluster()
		if err != nil {
			return errors.Wrap(err, "failed to delete ray cluster "+cluster.Metadata.Name)
		}
	}

	err := c.gw.DeleteCluster(cluster)
	if err != nil {
		return errors.Wrap(err, "failed to delete cluster backend service "+cluster.Metadata.Name)
	}

	err = c.updateStatus(cluster, nil, v1.ClusterPhaseDeleted, nil)
	if err != nil {
		klog.Errorf("failed to update cluster %s status, err: %v", cluster.Metadata.Name, err)
	}

	return nil
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

	if newStatus.Phase == v1.ClusterPhaseRunning && obj.Metadata.DeletionTimestamp == "" {
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
