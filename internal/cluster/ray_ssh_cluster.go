package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/command_runner"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var (
	ProvisioningWaitTime = 30 * time.Second
)

func init() { //nolint:gochecknoinits
	if v := os.Getenv("PROVISIONING_WAIT_TIME"); v != "" {
		if dur, err := time.ParseDuration(v); err == nil {
			ProvisioningWaitTime = dur
		}
	}
}

var _ ClusterReconcile = &sshRayClusterReconciler{}

type sshRayClusterReconciler struct {
	executor           command.Executor
	acceleratorManager accelerator.Manager
	storage            storage.Storage
}

func newRaySSHClusterReconcile(storage storage.Storage, acceleratorManager accelerator.Manager) *sshRayClusterReconciler {
	r := &sshRayClusterReconciler{
		acceleratorManager: acceleratorManager,
		storage:            storage,
	}

	if r.executor == nil {
		r.executor = &command.OSExecutor{}
	}

	return r
}

func (c *sshRayClusterReconciler) Reconcile(ctx context.Context, cluster *v1.Cluster) error {
	imageRegistry, err := getUsedImageRegistries(cluster, c.storage)
	if err != nil {
		return errors.Wrap(err, "failed to get used image registries")
	}

	sshClusterConfig, err := util.ParseSSHClusterConfig(cluster)
	if err != nil {
		return errors.Wrap(err, "failed to parse ssh cluster config")
	}

	reconcileCtx := &ReconcileContext{
		Ctx:              ctx,
		Cluster:          cluster,
		ImageRegistry:    imageRegistry,
		sshClusterConfig: sshClusterConfig,
	}

	err = c.generateConfig(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to generate config")
	}

	defer c.cleanupConfig(reconcileCtx) //nolint:errcheck

	if reconcileCtx.Cluster.Status == nil || !reconcileCtx.Cluster.Status.Initialized {
		err = c.initialize(reconcileCtx)
		if err != nil {
			return errors.Wrap(err, "failed to initialize cluster")
		}
	}

	reconcileCtx.rayService = c.getDashboardService(reconcileCtx.sshClusterConfig.Provider.HeadIP)

	defer func() {
		err = c.setClusterStatus(reconcileCtx)
		if err != nil {
			klog.Error(err, "failed to set cluster status")
		}
	}()

	err = c.reconcileHeadNode(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to reconcile head node")
	}

	err = c.reconcileWorkerNode(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to reconcile worker node")
	}

	return nil
}

func (c *sshRayClusterReconciler) setClusterStatus(reconcileCtx *ReconcileContext) error {
	clusterStatus, err := getRayClusterStatus(reconcileCtx.rayService)
	if err != nil {
		return errors.Wrap(err, "failed to get ray cluster status")
	}

	setClusterStatus(reconcileCtx.Cluster, clusterStatus)
	reconcileCtx.Cluster.Status.DesiredNodes += len(reconcileCtx.sshClusterConfig.Provider.WorkerIPs)

	// Collect cluster resources (including accelerators) if cluster is running
	if reconcileCtx.Cluster.Status.Phase == v1.ClusterPhaseRunning {
		resources, err := c.calculateClusterResources(reconcileCtx)
		if err != nil {
			return errors.Wrap(err, "failed to calculate cluster resources")
		}

		reconcileCtx.Cluster.Status.ResourceInfo = resources
	}

	return nil
}

func (c *sshRayClusterReconciler) ReconcileDelete(ctx context.Context, cluster *v1.Cluster) error {
	imageRegistry, err := getUsedImageRegistries(cluster, c.storage)
	if err != nil {
		return errors.Wrapf(err, "failed to get used image registry")
	}

	sshClusterConfig, err := util.ParseSSHClusterConfig(cluster)
	if err != nil {
		return errors.Wrap(err, "failed to parse ssh cluster config")
	}

	reconcileCtx := &ReconcileContext{
		Ctx:              ctx,
		Cluster:          cluster,
		ImageRegistry:    imageRegistry,
		sshClusterConfig: sshClusterConfig,
	}

	err = c.generateConfig(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to generate config")
	}

	defer c.cleanupConfig(reconcileCtx) //nolint:errcheck

	err = c.downCluster(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to down cluster")
	}

	return nil
}

func (c *sshRayClusterReconciler) generateConfig(reconcileCtx *ReconcileContext) error {
	if reconcileCtx.sshClusterConfig.Provider.HeadIP == "" {
		return errors.New("head IP can not be empty")
	}

	err := c.configClusterAcceleratorType(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to config cluster accelerator type")
	}

	rayClusterConfig, err := c.generateRayClusterConfig(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to generate ray cluster config")
	}

	reconcileCtx.sshRayClusterConfig = rayClusterConfig
	reconcileCtx.sshConfigGenerator = newRaySSHLocalConfigGenerator(reconcileCtx.Cluster.GetName())

	if err := reconcileCtx.sshConfigGenerator.Generate(rayClusterConfig); err != nil {
		return errors.Wrap(err, "failed to generate config")
	}

	return nil
}

func (c *sshRayClusterReconciler) cleanupConfig(reconcileCtx *ReconcileContext) error {
	if reconcileCtx.sshConfigGenerator != nil {
		return reconcileCtx.sshConfigGenerator.Cleanup()
	}

	return nil
}

func (c *sshRayClusterReconciler) reconcileHeadNode(reconcileCtx *ReconcileContext) error {
	_, err := reconcileCtx.rayService.GetClusterMetadata()
	if err == nil {
		return nil
	}

	klog.Infof("Head node not ready, try to up cluster %s", reconcileCtx.Cluster.Metadata.WorkspaceName())

	return c.initialize(reconcileCtx)
}

func (c *sshRayClusterReconciler) reconcileWorkerNode(reconcileCtx *ReconcileContext) error { //nolint:gocyclo
	klog.V(4).Info("Reconciling Static Nodes for cluster " + reconcileCtx.Cluster.Metadata.WorkspaceName())

	var (
		desiredStaticNodeIpMap       = map[string]string{}
		staticNodeProvisionStatusMap = map[string]v1.NodeProvision{}
		currentNodeStatusMap         = map[string]string{}
		nodeIpToStart                []string
		nodeIpToStop                 []string
	)

	// get desired static provision node ip from cluster spec
	desiredStaticWorkersIP := reconcileCtx.sshClusterConfig.Provider.WorkerIPs

	for _, nodeIp := range desiredStaticWorkersIP {
		desiredStaticNodeIpMap[nodeIp] = nodeIp
	}

	nodeList, err := reconcileCtx.rayService.ListNodes()
	if err != nil {
		return errors.Wrap(err, "failed to list ray nodes")
	}

	for _, node := range nodeList {
		if node.Raylet.IsHeadNode {
			continue
		}

		// ray will record all node state, even the node is not alive, and if the node restart, it will create a new node id.
		// so we need to check the node state to get the current alive node status.
		if state, ok := currentNodeStatusMap[node.IP]; !ok || state != node.Raylet.State {
			currentNodeStatusMap[node.IP] = node.Raylet.State
		}
	}

	// get static node provision status from cluster status
	if reconcileCtx.Cluster.Status != nil && reconcileCtx.Cluster.Status.NodeProvisionStatus != "" {
		err := json.Unmarshal([]byte(reconcileCtx.Cluster.Status.NodeProvisionStatus), &staticNodeProvisionStatusMap)
		if err != nil {
			return errors.Wrap(err, "failed to unmarshal static node provision status")
		}
	}

	// check which node need to start.
	// 1. the node in desired node list, but not in provision status map, need to start.
	// 2. the node in desired node list, and in provision status map, but the provision status is not "Provisioned", need to start.
	// 3. the node in desired node list, and in provision status map, and the provision status is "Provisioned", but the current node state is not "ALIVE", need to start.
	checkNeedStart := func(nodeIp string) bool {
		provisionStatus, ok := staticNodeProvisionStatusMap[nodeIp]
		if !ok {
			return true
		}

		if provisionStatus.Status != v1.ProvisionedNodeProvisionStatus {
			return true
		}

		if state, ok := currentNodeStatusMap[nodeIp]; !ok || state != v1.AliveNodeState {
			lastProvisionTime, err := util.ParseTime(provisionStatus.LastProvisionTime)
			if err != nil {
				klog.Warningf("Failed to parse provision time %s for node %s, err: %v", provisionStatus.LastProvisionTime, nodeIp, err)
				return false
			}

			if time.Since(lastProvisionTime) < ProvisioningWaitTime {
				klog.Infof("Node %s was just provisioned at %s, skip restarting", nodeIp, provisionStatus.LastProvisionTime)
				return false
			}

			klog.Infof("Node %s current state is %s, need to restart", nodeIp, state)

			return true
		}

		return false
	}

	// check which node need to stop.
	// 1. the node not in desired node list, but in provision status map, need to stop.
	checkNeedStop := func(nodeIp string) bool {
		if _, ok := desiredStaticNodeIpMap[nodeIp]; !ok {
			return true
		}

		return false
	}

	for nodeIp, nodeProvision := range staticNodeProvisionStatusMap {
		if nodeProvision.IsHead {
			continue
		}

		if checkNeedStop(nodeIp) {
			nodeIpToStop = append(nodeIpToStop, nodeIp)
		}
	}

	for _, nodeIp := range desiredStaticNodeIpMap {
		if checkNeedStart(nodeIp) {
			nodeIpToStart = append(nodeIpToStart, nodeIp)
		}
	}

	nodeOpErrors := make([]error, len(nodeIpToStart)+len(nodeIpToStop))
	eg := &errgroup.Group{}

	for i := range nodeIpToStart {
		ip := nodeIpToStart[i]

		eg.Go(func() error {
			klog.Infof("Starting ray node %s for cluster %s", ip, reconcileCtx.Cluster.Metadata.WorkspaceName())

			err := c.startNode(reconcileCtx, ip)
			if err != nil {
				nodeOpErrors[i] = errors.Wrap(err, "failed to start ray node "+ip)
			}

			klog.Infof("Ray node %s started successfully for cluster %s", ip, reconcileCtx.Cluster.Metadata.WorkspaceName())

			return nil
		})
	}

	for i := range nodeIpToStop {
		ip := nodeIpToStop[i]

		eg.Go(func() error {
			klog.Infof("Stopping ray node %s for cluster %s", ip, reconcileCtx.Cluster.Metadata.WorkspaceName())

			err := c.stopNode(reconcileCtx, ip, false)
			if err != nil {
				nodeOpErrors[i+len(nodeIpToStart)] = errors.Wrap(err, "failed to stop ray node "+ip)
			}

			klog.Infof("Ray node %s stopped successfully for cluster %s", ip, reconcileCtx.Cluster.Metadata.WorkspaceName())

			return nil
		})
	}

	eg.Wait() //nolint:errcheck

	// update static node provision status
	for i := range nodeIpToStart {
		if nodeOpErrors[i] == nil {
			staticNodeProvisionStatusMap[nodeIpToStart[i]] = v1.NodeProvision{
				LastProvisionTime: time.Now().Format(time.RFC3339),
				Status:            v1.ProvisionedNodeProvisionStatus,
				IsHead:            false,
			}
		} else {
			staticNodeProvisionStatusMap[nodeIpToStart[i]] = v1.NodeProvision{
				LastProvisionTime: time.Now().Format(time.RFC3339),
				Status:            v1.ProvisioningNodeProvisionStatus,
				IsHead:            false,
			}
		}
	}

	for i := range nodeIpToStop {
		if nodeOpErrors[len(nodeIpToStart)+i] == nil {
			delete(staticNodeProvisionStatusMap, nodeIpToStop[i])
		}
	}

	// update cluster status
	staticNodeProvisionStatusContent, err := json.Marshal(staticNodeProvisionStatusMap)
	if err != nil {
		return errors.Wrap(err, "failed to marshal static node provision status")
	}

	if reconcileCtx.Cluster.Status == nil {
		reconcileCtx.Cluster.Status = &v1.ClusterStatus{}
	}

	reconcileCtx.Cluster.Status.NodeProvisionStatus = string(staticNodeProvisionStatusContent)

	aggregateError := apierrors.NewAggregate(nodeOpErrors)
	if aggregateError != nil {
		return aggregateError
	}

	return nil
}

func (c *sshRayClusterReconciler) initialize(reconcileCtx *ReconcileContext) error {
	klog.Info("Start to initialize cluster " + reconcileCtx.Cluster.Metadata.WorkspaceName())

	if reconcileCtx.Cluster.Status == nil {
		reconcileCtx.Cluster.Status = &v1.ClusterStatus{}
	}

	if !reconcileCtx.Cluster.Status.Initialized {
		reconcileCtx.Cluster.Status.Phase = v1.ClusterPhaseInitializing

		err := c.storage.UpdateCluster(strconv.Itoa(reconcileCtx.Cluster.ID), &v1.Cluster{
			Status: reconcileCtx.Cluster.Status,
		})
		if err != nil {
			return errors.Wrap(err, "failed to update cluster status")
		}
	}

	provisioned, lastProvisionTime, err := getNodeLastProvisionTime(reconcileCtx, reconcileCtx.sshClusterConfig.Provider.HeadIP)
	if err != nil {
		return errors.Wrap(err, "failed to get head node last provision time")
	}

	if provisioned {
		if time.Since(lastProvisionTime) < ProvisioningWaitTime {
			klog.Infof("Head node %s was just provisioned at %s, skip initializing", reconcileCtx.sshClusterConfig.Provider.HeadIP, lastProvisionTime.Format(time.RFC3339))
			return errors.New("head node just provisioned, waiting for it to be ready")
		}
	}

	klog.Infof("Initializing cluster %s by uping the cluster", reconcileCtx.Cluster.Metadata.WorkspaceName())

	headIP, err := c.upCluster(reconcileCtx, false)
	if err != nil {
		return errors.Wrap(err, "failed to up cluster")
	}

	err = setNodePrivisionStatus(reconcileCtx, headIP, v1.ProvisionedNodeProvisionStatus, true)
	if err != nil {
		klog.Warningf("Failed to set head node provision status: %v", err)
	}

	dashboardUrl := fmt.Sprintf("http://%s:8265", headIP)
	dashboardSvc := dashboard.NewDashboardService(dashboardUrl)

	_, err = dashboardSvc.GetClusterMetadata()
	if err != nil {
		return errors.Wrap(err, "failed to get cluster metadata")
	}

	reconcileCtx.Cluster.Status.Initialized = true
	reconcileCtx.Cluster.Status.DashboardURL = dashboardUrl

	err = c.storage.UpdateCluster(strconv.Itoa(reconcileCtx.Cluster.ID), &v1.Cluster{
		Status: reconcileCtx.Cluster.Status,
	})
	if err != nil {
		return errors.Wrap(err, "failed to update cluster status")
	}

	klog.Info("Cluster " + reconcileCtx.Cluster.Metadata.WorkspaceName() + " initialized successfully")

	return nil
}

func (c *sshRayClusterReconciler) buildSSHCommandArgs(reconcileCtx *ReconcileContext, nodeIP string) *command_runner.CommonArgs {
	return &command_runner.CommonArgs{
		NodeID: nodeIP,
		SshIP:  nodeIP,
		AuthConfig: v1.Auth{
			SSHUser:       reconcileCtx.sshClusterConfig.Auth.SSHUser,
			SSHPrivateKey: reconcileCtx.sshConfigGenerator.SSHKeyPath(),
		},
		SSHControlPath: "",
		ProcessExecute: c.executor.Execute,
	}
}

func (c *sshRayClusterReconciler) configClusterAcceleratorType(reconcileCtx *ReconcileContext) error {
	acceleratorType, err := c.detectClusterAcceleratorType(reconcileCtx)
	if err != nil {
		return errors.Wrapf(err, "failed to detect cluster accelerator type")
	}

	// record detected accelerator type to cluster status.
	if reconcileCtx.Cluster.Status == nil {
		reconcileCtx.Cluster.Status = &v1.ClusterStatus{}
	}

	reconcileCtx.Cluster.Status.AcceleratorType = &acceleratorType

	return nil
}

func (c *sshRayClusterReconciler) detectClusterAcceleratorType(reconcileCtx *ReconcileContext) (string, error) {
	// first we check the cluster spec for accelerator type (now in ClusterConfig level)
	if reconcileCtx.Cluster.Spec != nil && reconcileCtx.Cluster.Spec.Config != nil &&
		reconcileCtx.Cluster.Spec.Config.AcceleratorType != nil {
		return *reconcileCtx.Cluster.Spec.Config.AcceleratorType, nil
	}

	// Check cached accelerator type, but only trust non-empty values
	// Empty cached value should trigger re-detection to support dynamic GPU node addition
	if reconcileCtx.Cluster.Status != nil && reconcileCtx.Cluster.Status.AcceleratorType != nil &&
		*reconcileCtx.Cluster.Status.AcceleratorType != "" {
		return *reconcileCtx.Cluster.Status.AcceleratorType, nil
	}

	detectAcceleratorType := ""

	// finally we detect the accelerator type from nodes
	acceleratorType, err := c.acceleratorManager.GetNodeAcceleratorType(context.Background(),
		reconcileCtx.sshClusterConfig.Provider.HeadIP, reconcileCtx.sshClusterConfig.Auth)
	if err != nil {
		return "", errors.Wrap(err, "failed to get node accelerator type")
	}

	detectAcceleratorType = acceleratorType

	for _, workerIP := range reconcileCtx.sshClusterConfig.Provider.WorkerIPs {
		acceleratorType, err = c.acceleratorManager.GetNodeAcceleratorType(context.Background(), workerIP, reconcileCtx.sshClusterConfig.Auth)
		if err != nil {
			return "", errors.Wrap(err, "failed to get node accelerator type")
		}

		// Skip CPU-only nodes - they can coexist with accelerator nodes
		if acceleratorType == "" {
			continue
		}

		if detectAcceleratorType == "" {
			detectAcceleratorType = acceleratorType
			continue
		}

		// If both detectAcceleratorType and acceleratorType are non-empty,
		// ensure they match (we don't support mixed accelerator types like NVIDIA + AMD)
		if detectAcceleratorType != acceleratorType {
			return "", errors.New("cluster has different accelerator type")
		}
	}

	return detectAcceleratorType, nil
}

func getNodeLastProvisionTime(reconcileCtx *ReconcileContext, nodeIP string) (bool, time.Time, error) {
	var staticNodeProvisionStatusMap = map[string]v1.NodeProvision{}

	if reconcileCtx.Cluster.Status != nil && reconcileCtx.Cluster.Status.NodeProvisionStatus != "" {
		err := json.Unmarshal([]byte(reconcileCtx.Cluster.Status.NodeProvisionStatus), &staticNodeProvisionStatusMap)
		if err != nil {
			return false, time.Time{}, errors.Wrap(err, "failed to unmarshal static node provision status")
		}
	}

	privision, ok := staticNodeProvisionStatusMap[nodeIP]
	if !ok {
		return false, time.Time{}, nil
	}

	provisionTime, err := util.ParseTime(privision.LastProvisionTime)
	if err != nil {
		return false, time.Time{}, errors.Wrapf(err, "failed to parse privision time")
	}

	return ok, provisionTime, nil
}

func setNodePrivisionStatus(reconcileCtx *ReconcileContext, nodeIP, status string, isHead bool) error {
	var staticNodeProvisionStatusMap = map[string]v1.NodeProvision{}

	if reconcileCtx.Cluster.Status != nil && reconcileCtx.Cluster.Status.NodeProvisionStatus != "" {
		err := json.Unmarshal([]byte(reconcileCtx.Cluster.Status.NodeProvisionStatus), &staticNodeProvisionStatusMap)
		if err != nil {
			return errors.Wrap(err, "failed to unmarshal static node provision status")
		}
	}

	privision := v1.NodeProvision{}
	privision.Status = status
	privision.LastProvisionTime = time.Now().Format(time.RFC3339Nano)
	privision.IsHead = isHead
	staticNodeProvisionStatusMap[nodeIP] = privision

	staticNodeProvisionStatusContent, err := json.Marshal(staticNodeProvisionStatusMap)
	if err != nil {
		return errors.Wrap(err, "failed to marshal static node provision status")
	}

	if reconcileCtx.Cluster.Status == nil {
		reconcileCtx.Cluster.Status = &v1.ClusterStatus{}
	}

	reconcileCtx.Cluster.Status.NodeProvisionStatus = string(staticNodeProvisionStatusContent)

	return nil
}

func (c *sshRayClusterReconciler) calculateClusterResources(
	reconcileCtx *ReconcileContext,
) (*v1.ClusterResources, error) {
	// Create dashboard service client
	dashboardSvc := reconcileCtx.rayService
	// Fetch node list from Dashboard
	nodeList, err := dashboardSvc.ListNodes()
	if err != nil {
		return nil, fmt.Errorf("failed to get node list from Ray Dashboard: %w", err)
	}

	availableResource := map[string]float64{}
	allocatableResource := map[string]float64{}
	nodeResources := map[string]*v1.ResourceStatus{}

	for _, node := range nodeList {
		if node.Raylet.State != v1.AliveNodeState {
			continue
		}

		nodeAvailableResource := map[string]float64{}
		nodeAllocatableResource := map[string]float64{}

		for resourceKey, quantity := range node.Raylet.Resources {
			nodeAllocatableResource[resourceKey] = quantity
			nodeAvailableResource[resourceKey] = quantity
		}

		for _, workers := range node.Raylet.CoreWorkersStats {
			for resourceKey, allocations := range workers.UsedResources {
				nodeAvailableResource[resourceKey] -= float64(allocations.TotalAllocation())
			}
		}

		klog.V(4).Infof("Node %s allocatable resources: %+v", node.IP, nodeAllocatableResource)
		klog.V(4).Infof("Node %s available resources: %+v", node.IP, nodeAvailableResource)

		nodeResourceStatus, err := c.transformResources(nodeAvailableResource, nodeAllocatableResource)
		if err != nil {
			return nil, fmt.Errorf("failed to transform resources for node %s: %w", node.IP, err)
		}

		nodeResources[node.IP] = nodeResourceStatus

		for resourceKey, quantity := range nodeAvailableResource {
			availableResource[resourceKey] += quantity
		}

		for resourceKey, quantity := range nodeAllocatableResource {
			allocatableResource[resourceKey] += quantity
		}
	}

	clusterResourceStatus, err := c.transformResources(availableResource, allocatableResource)
	if err != nil {
		return nil, fmt.Errorf("failed to transform cluster resources: %w", err)
	}

	result := &v1.ClusterResources{
		ResourceStatus: *clusterResourceStatus,
		NodeResources:  nodeResources,
	}

	return result, nil
}

func (c *sshRayClusterReconciler) transformResources(availableResource, allocatableResource map[string]float64) (*v1.ResourceStatus, error) {
	availableResourceCPU, ok := availableResource["CPU"]
	if ok {
		if availableResourceCPU < 0 {
			availableResourceCPU = 0
		} else {
			availableResourceCPU = roundFloat64ToTwoDecimals(availableResourceCPU)
		}
	}

	availableResourceMemory, ok := availableResource["memory"]
	if ok {
		if availableResourceMemory < 0 {
			availableResourceMemory = 0
		} else {
			availableResourceMemory = roundFloat64ToTwoDecimals(availableResourceMemory / plugin.BytesPerGiB)
		}
	}

	allocatableResourceCPU, ok := allocatableResource["CPU"]
	if ok {
		if allocatableResourceCPU < 0 {
			allocatableResourceCPU = 0
		} else {
			allocatableResourceCPU = roundFloat64ToTwoDecimals(allocatableResourceCPU)
		}
	}

	allocatableResourceMemory, ok := allocatableResource["memory"]
	if ok {
		if allocatableResourceMemory < 0 {
			allocatableResourceMemory = 0
		} else {
			allocatableResourceMemory = roundFloat64ToTwoDecimals(allocatableResourceMemory / plugin.BytesPerGiB)
		}
	}

	result := &v1.ResourceStatus{
		Allocatable: &v1.ResourceInfo{
			CPU:               allocatableResourceCPU,
			Memory:            allocatableResourceMemory,
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
		Available: &v1.ResourceInfo{
			CPU:               availableResourceCPU,
			Memory:            availableResourceMemory,
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
	}

	resourceParserMap := c.acceleratorManager.GetAllParsers()
	for resourceKey, parser := range resourceParserMap {
		allocatableInfo, err := parser.ParseFromRay(allocatableResource)
		if err != nil {
			return nil, fmt.Errorf("failed to parse allocatable resources from Ray for resource %s: %w", resourceKey, err)
		}

		if allocatableInfo != nil && len(allocatableInfo.AcceleratorGroups) > 0 {
			for key, group := range allocatableInfo.AcceleratorGroups {
				if existingGroup, exists := result.Allocatable.AcceleratorGroups[key]; exists {
					existingGroup.Quantity += group.Quantity
					for productKey, quantity := range group.ProductGroups {
						if existingQuantity, exists := existingGroup.ProductGroups[productKey]; exists {
							existingGroup.ProductGroups[productKey] = existingQuantity + quantity
						} else {
							existingGroup.ProductGroups[productKey] = quantity
						}
					}

					result.Allocatable.AcceleratorGroups[key] = existingGroup
				} else {
					result.Allocatable.AcceleratorGroups[key] = allocatableInfo.AcceleratorGroups[key]
				}
			}
		}

		availableInfo, err := parser.ParseFromRay(availableResource)
		if err != nil {
			return nil, fmt.Errorf("failed to parse available resources from Ray for resource %s: %w", resourceKey, err)
		}

		if availableInfo != nil && len(availableInfo.AcceleratorGroups) > 0 {
			for key, group := range availableInfo.AcceleratorGroups {
				if existingGroup, exists := result.Available.AcceleratorGroups[key]; exists {
					existingGroup.Quantity += group.Quantity
					for productKey, quantity := range group.ProductGroups {
						if existingQuantity, exists := existingGroup.ProductGroups[productKey]; exists {
							existingGroup.ProductGroups[productKey] = existingQuantity + quantity
						} else {
							existingGroup.ProductGroups[productKey] = quantity
						}
					}

					result.Available.AcceleratorGroups[key] = existingGroup
				} else {
					result.Available.AcceleratorGroups[key] = availableInfo.AcceleratorGroups[key]
				}
			}
		}
	}

	return result, nil
}
