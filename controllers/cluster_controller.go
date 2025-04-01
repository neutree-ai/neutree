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
	"github.com/neutree-ai/neutree/internal/orchestrator"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ClusterController struct {
	baseController *BaseController

	storage               storage.Storage
	defaultClusterVersion string
}

type ClusterControllerOption struct {
	Storage              storage.Storage
	Workers              int
	DefaultClusterVesion string
}

func NewClusterController(opt *ClusterControllerOption) (*ClusterController, error) {
	c := &ClusterController{
		baseController: &BaseController{
			queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "cluster"}),
			workers:      opt.Workers,
			syncInterval: time.Second * 10,
		},
		storage:               opt.Storage,
		defaultClusterVersion: opt.DefaultClusterVesion,
	}

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

	return c.sync(obj)
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
// 2. get static node provision status from cluster status
// 3. compare desired static node ip and static node provision status
// 4. start static node that not provisioned
// 5. stop static node that not in desired static node ip
func (c *ClusterController) ReconcileStaticNodes(cluster *v1.Cluster, clusterOrchestrator orchestrator.Orchestrator) error { //nolint:funlen
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
