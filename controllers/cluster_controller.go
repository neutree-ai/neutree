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
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ClusterController struct {
	storage storage.Storage
	queue   workqueue.RateLimitingInterface
	workers int

	syncInterval time.Duration

	defaultClusterVersion string
}

type ClusterControllerOption struct {
	Storage              storage.Storage
	Workers              int
	DefaultClusterVesion string
}

func NewClusterController(opt *ClusterControllerOption) (*ClusterController, error) {
	c := &ClusterController{
		storage: opt.Storage,
		queue: workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(),
			workqueue.RateLimitingQueueConfig{Name: "cluster-controller"}),
		workers:               opt.Workers,
		syncInterval:          time.Second * 10,
		defaultClusterVersion: opt.DefaultClusterVesion,
	}

	return c, nil
}

func (c *ClusterController) Start(ctx context.Context) {
	klog.Infof("Starting cluster controller")

	defer c.queue.ShutDown()

	for i := 0; i < c.workers; i++ {
		go wait.UntilWithContext(ctx, c.worker, time.Second)
	}

	wait.Until(c.reconcileAll, c.syncInterval, ctx.Done())
	<-ctx.Done()
}

func (c *ClusterController) worker(ctx context.Context) { //nolint:unparam
	for c.processNextWorkItem() {
	}
}

func (c *ClusterController) processNextWorkItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	clusterID, ok := key.(int)
	if !ok {
		klog.Error("failed to assert key to clusterID")
		return true
	}

	obj, err := c.storage.GetCluster(strconv.Itoa(clusterID))
	if err != nil {
		klog.Errorf("failed to get cluster %s, err: %v", strconv.Itoa(clusterID), err)
		return true
	}

	klog.V(4).Info("Reconciling cluster " + obj.Metadata.Name)

	err = c.sync(obj)
	if err != nil {
		klog.Errorf("failed to sync cluster %s, err: %v", obj.Metadata.Name, err)
		return true
	}

	return true
}

func (c *ClusterController) reconcileAll() {
	clusters, err := c.storage.ListCluster(storage.ListOption{})
	if err != nil {
		klog.Errorf("failed to list clusters, err: %v", err)
		return
	}

	for i := range clusters {
		c.queue.Add(clusters[i].ID)
	}
}

func (c *ClusterController) sync(obj *v1.Cluster) error {
	var (
		err error
	)

	if obj.Spec.Version == "" {
		obj.Spec.Version = c.defaultClusterVersion
	}

	imageRegistry, err := c.getRelateImageRegistry(obj)
	if err != nil {
		return err
	}

	clusterOrchestrator, err := orchestrator.NewOrchestrator(orchestrator.Options{
		Cluster:       obj,
		ImageRegistry: imageRegistry,
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

	// only ssh should reconcile static node.
	if cluster.Spec.Type == "ssh" {
		err = c.ReconcileStaticNodes(cluster, clusterOrchestrator)
		if err != nil {
			return errors.Wrap(err, "failed to reconcile cluster static node "+cluster.Metadata.Name)
		}
	}

	err = clusterOrchestrator.HealthCheck()
	if err != nil {
		return errors.Wrap(err, "health check cluster failed")
	}

	return nil
}

// 1. get desired static node ip from cluster spec
// 2. get static node provision status from cluster labels
// 3. compare desired static node ip and static node provision status
// 4. start static node that not provisioned
// 5. stop static node that not in desired static node ip
func (c *ClusterController) ReconcileStaticNodes(cluster *v1.Cluster, clusterOrchestrator orchestrator.Orchestrator) error { //nolint:funlen
	klog.V(4).Info("Reconciling Static Nodes for cluster " + cluster.Metadata.Name)

	var (
		desiredStaticNodeIpMap    = map[string]string{}
		staticNodeProvisionStatus = map[string]string{}
		nodeIpToStart             []string
		nodeIpToStop              []string
	)

	// get desired static provision node ip from cluster spec
	desiredStaticWorkersIP := clusterOrchestrator.GetDesireStaticWorkersIP()

	for _, nodeIp := range desiredStaticWorkersIP {
		desiredStaticNodeIpMap[nodeIp] = nodeIp
	}

	// get static node provision status from cluster labels
	if cluster.Metadata.Labels == nil {
		cluster.Metadata.Labels = map[string]string{}
	}

	if cluster.Metadata.Labels[v1.NeutreeNodeProvisionStatusLabel] != "" {
		err := json.Unmarshal([]byte(cluster.Metadata.Labels[v1.NeutreeNodeProvisionStatusLabel]), &staticNodeProvisionStatus)
		if err != nil {
			return errors.Wrap(err, "failed to unmarshal static node provision status")
		}
	}

	for nodeIp := range staticNodeProvisionStatus {
		if _, ok := desiredStaticNodeIpMap[nodeIp]; ok {
			continue
		}

		nodeIpToStop = append(nodeIpToStop, nodeIp)
	}

	for _, nodeIp := range desiredStaticNodeIpMap {
		// already provisioned, skip
		if _, ok := staticNodeProvisionStatus[nodeIp]; ok && staticNodeProvisionStatus[nodeIp] == v1.ProvisionedNodeProvisionStatus {
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
			staticNodeProvisionStatus[nodeIpToStart[i]] = v1.ProvisionedNodeProvisionStatus
		} else {
			staticNodeProvisionStatus[nodeIpToStart[i]] = v1.ProvisioningNodeProvisionStatus
		}
	}

	for i := range nodeIpToStop {
		if nodeOpErrors[len(nodeIpToStart)+i] == nil {
			delete(staticNodeProvisionStatus, nodeIpToStop[i])
		}
	}

	// update cluster labels
	staticNodeProvisionStatusContent, err := json.Marshal(staticNodeProvisionStatus)
	if err != nil {
		return errors.Wrap(err, "failed to marshal static node provision status")
	}

	cluster.Metadata.Labels[v1.NeutreeNodeProvisionStatusLabel] = string(staticNodeProvisionStatusContent)

	err = c.storage.UpdateCluster(strconv.Itoa(cluster.ID), &v1.Cluster{Metadata: cluster.Metadata})
	if err != nil {
		return errors.Wrap(err, "failed to update cluster "+cluster.Metadata.Name)
	}

	aggregateError := apierrors.NewAggregate(nodeOpErrors)
	if aggregateError != nil {
		return aggregateError
	}

	return nil
}

func (c *ClusterController) reconcileDelete(cluster *v1.Cluster, clusterOrchestrator orchestrator.Orchestrator) error {
	if cluster.Status.Phase == v1.ClusterPhaseDeleted {
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
	imageRegistryList, err := c.storage.ListImageRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    fmt.Sprintf(`"%s"`, cluster.Spec.ImageRegistry),
			},
		},
	})
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
	}

	if obj.IsInitialized() {
		clusterStatus, getClusterStatusErr := clusterOrchestrator.ClusterStatus()
		if getClusterStatusErr != nil {
		} else {
			newStatus.ReadyNodes = clusterStatus.ReadyNodes
			newStatus.DesiredNodes = len(clusterOrchestrator.GetDesireStaticWorkersIP())
			newStatus.Version = clusterStatus.NeutreeServeVersion
			newStatus.RayVersion = clusterStatus.RayVersion
		}
	}

	if err != nil {
		newStatus.ErrorMessage = err.Error()
	}

	return c.storage.UpdateCluster(strconv.Itoa(obj.ID), &v1.Cluster{Status: newStatus})
}
