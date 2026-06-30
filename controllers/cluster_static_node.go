package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	staticNodeClusterFlowVersionGate = "v1.0.1"
	staticNodeClusterDashboardPort   = 8265
	staticForceDeleteAnnotationKey   = "neutree.ai/force-delete"
	staticForceDeleteAnnotationValue = "true"
)

func shouldUseStaticNodeClusterFlow(c *v1.Cluster) (bool, error) {
	if c == nil || c.Spec == nil || c.Spec.Type != v1.SSHClusterType {
		return false, nil
	}

	return isStaticNodeClusterFlowVersion(c.GetVersion())
}

func (controller *ClusterController) reconcileStaticNodeCluster(c *v1.Cluster) error {
	if err := validateStaticNodeClusterSpec(c); err != nil {
		return err
	}

	current, found, err := controller.findStaticNodeCluster(c.Metadata.Workspace, c.Metadata.Name)
	if err != nil {
		return errors.Wrap(err, "failed to find static node cluster")
	}

	if !found {
		if err := controller.cleanupLegacyRuntimeBeforeStaticNodeFlow(c); err != nil {
			return err
		}
	}

	desired, err := controller.buildStaticNodeCluster(c)
	if err != nil {
		return err
	}

	if !found {
		if err := controller.storage.CreateStaticNodeCluster(desired); err != nil {
			return errors.Wrap(err, "failed to create static node cluster")
		}

		controller.copyStaticNodeClusterStatus(c, desired, nil)

		return withClusterPhaseOverride(
			errors.Errorf("static node cluster %s is provisioning", c.Metadata.Name),
			staticNodeClusterProgressPhase(c, nil),
		)
	}

	desired.ID = current.ID
	if err := controller.storage.UpdateStaticNodeCluster(strconv.Itoa(current.ID), desired); err != nil {
		return errors.Wrap(err, "failed to update static node cluster")
	}

	controller.copyStaticNodeClusterStatus(c, desired, current.Status)

	if current.Status == nil || current.Status.Phase != v1.StaticNodeClusterPhaseReady {
		return withClusterPhaseOverride(
			staticNodeClusterNotReadyError(current),
			staticNodeClusterProgressPhase(c, current.Status),
		)
	}

	return nil
}

func validateStaticNodeClusterSpec(c *v1.Cluster) error {
	if c == nil || c.Metadata == nil {
		return errors.New("cluster metadata is required")
	}

	if c.Spec == nil {
		return errors.New("cluster spec is required")
	}

	if c.Spec.Version == "" {
		return errors.New("cluster spec.version is required")
	}

	sshConfig, err := util.ParseSSHClusterConfig(c)
	if err != nil {
		return errors.Wrap(err, "failed to parse ssh cluster config")
	}

	if sshConfig.Provider.HeadIP == "" {
		return errors.New("head IP can not be empty")
	}

	nodeIPs := map[string]struct{}{
		sshConfig.Provider.HeadIP: {},
	}

	for _, workerIP := range sshConfig.Provider.WorkerIPs {
		if workerIP == "" {
			continue
		}

		if _, exists := nodeIPs[workerIP]; exists {
			return errors.Errorf("duplicate static node IP %s", workerIP)
		}

		nodeIPs[workerIP] = struct{}{}
	}

	if sshConfig.Auth.SSHUser == "" {
		return errors.New("ssh_user is required")
	}

	if sshConfig.Auth.SSHPrivateKey == "" {
		return errors.New("ssh_private_key is required")
	}

	return nil
}

func (controller *ClusterController) reconcileStaticNodeClusterDelete(c *v1.Cluster) error {
	current, found, err := controller.findStaticNodeCluster(c.Metadata.Workspace, c.Metadata.Name)
	if err != nil {
		return errors.Wrap(err, "failed to find static node cluster")
	}

	if !found {
		return nil
	}

	if current.Metadata == nil {
		current.Metadata = &v1.Metadata{}
	}

	metadataChanged := false

	if v1.IsForceDelete(c.Metadata.Annotations) && !v1.IsForceDelete(current.Metadata.Annotations) {
		current.Metadata.Annotations = withForceDeleteAnnotation(current.Metadata.Annotations)
		metadataChanged = true
	}

	if current.Metadata.DeletionTimestamp == "" {
		current.Metadata.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)
		metadataChanged = true
	}

	if metadataChanged {
		if err := controller.storage.UpdateStaticNodeCluster(strconv.Itoa(current.ID), current); err != nil {
			return errors.Wrap(err, "failed to mark static node cluster deleting")
		}
	}

	return errors.Errorf("static node cluster %s is deleting", current.Metadata.Name)
}

func withForceDeleteAnnotation(annotations map[string]string) map[string]string {
	next := make(map[string]string, len(annotations)+1)
	for key, value := range annotations {
		next[key] = value
	}

	next[staticForceDeleteAnnotationKey] = staticForceDeleteAnnotationValue

	return next
}

func (controller *ClusterController) shouldUseStaticNodeClusterDeleteFlow(c *v1.Cluster) (bool, error) {
	if c == nil || c.Spec == nil || c.Spec.Type != v1.SSHClusterType {
		return false, nil
	}

	useStaticNodeFlow, err := shouldUseStaticNodeClusterFlow(c)
	if err != nil {
		return false, err
	}

	if useStaticNodeFlow {
		return true, nil
	}

	if c.Metadata == nil {
		return false, nil
	}

	_, found, err := controller.findStaticNodeCluster(c.Metadata.Workspace, c.Metadata.Name)
	if err != nil {
		return false, err
	}

	return found, nil
}

func (controller *ClusterController) validateStaticNodeClusterUpdate(c *v1.Cluster) error {
	if c == nil || !c.IsInitialized() {
		return nil
	}

	desiredHeadIP, err := staticClusterSpecHeadIP(c)
	if err != nil {
		return err
	}

	currentHeadIP := currentStaticClusterHeadIP(c)
	if desiredHeadIP == "" || currentHeadIP == "" || desiredHeadIP == currentHeadIP {
		return nil
	}

	return fmt.Errorf("initialized static cluster head ip can not be changed from %s to %s", currentHeadIP, desiredHeadIP)
}

func (controller *ClusterController) cleanupLegacyRuntimeBeforeStaticNodeFlow(c *v1.Cluster) error {
	if !isLegacyToStaticNodeFlowUpgrade(c) {
		return nil
	}

	if controller.cleanupLegacyStaticRuntime != nil {
		return controller.cleanupLegacyStaticRuntime(c)
	}

	reconciler, err := controller.newClusterReconcile(c, controller.acceleratorManager, controller.storage, controller.metricsRemoteWriteURL)
	if err != nil {
		return errors.Wrap(err, "failed to create legacy static runtime cleaner")
	}

	cleaner, ok := reconciler.(interface {
		CleanupLegacyRuntime(ctx context.Context, cluster *v1.Cluster) error
	})
	if !ok {
		return errors.New("legacy static runtime cleaner is not supported")
	}

	if err := cleaner.CleanupLegacyRuntime(context.Background(), c); err != nil {
		return errors.Wrap(err, "failed to cleanup legacy static runtime")
	}

	return nil
}

func (controller *ClusterController) cleanupStaticNodeClusterBeforeLegacyFlow(c *v1.Cluster) error {
	if !isStaticNodeToLegacyFlowRollback(c) {
		return nil
	}

	return controller.reconcileStaticNodeClusterDelete(c)
}

func isLegacyToStaticNodeFlowUpgrade(c *v1.Cluster) bool {
	if c == nil || c.Status == nil || !c.Status.Initialized || c.Status.Version == "" {
		return false
	}

	wasStaticNodeFlow, err := isStaticNodeClusterFlowVersion(c.Status.Version)
	if err != nil || wasStaticNodeFlow {
		return false
	}

	useStaticNodeFlow, err := isStaticNodeClusterFlowVersion(c.GetVersion())
	if err != nil {
		return false
	}

	return useStaticNodeFlow
}

func isStaticNodeToLegacyFlowRollback(c *v1.Cluster) bool {
	if c == nil || c.Status == nil || !c.Status.Initialized || c.Status.Version == "" {
		return false
	}

	wasStaticNodeFlow, err := isStaticNodeClusterFlowVersion(c.Status.Version)
	if err != nil || !wasStaticNodeFlow {
		return false
	}

	useStaticNodeFlow, err := isStaticNodeClusterFlowVersion(c.GetVersion())
	if err != nil {
		return false
	}

	return !useStaticNodeFlow
}

func isStaticNodeClusterFlowVersion(version string) (bool, error) {
	useStaticNodeFlow, err := semver.LessThan(staticNodeClusterFlowVersionGate, version)
	if err != nil {
		return false, fmt.Errorf("invalid cluster version %q: %w", version, err)
	}

	return useStaticNodeFlow, nil
}

func staticClusterSpecHeadIP(c *v1.Cluster) (string, error) {
	sshConfig, err := util.ParseSSHClusterConfig(c)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse ssh cluster config")
	}

	return sshConfig.Provider.HeadIP, nil
}

func currentStaticClusterHeadIP(c *v1.Cluster) string {
	if c == nil || c.Status == nil {
		return ""
	}

	if c.Status.NodeProvisionStatus != "" {
		nodeStatus := map[string]v1.NodeProvision{}
		if err := json.Unmarshal([]byte(c.Status.NodeProvisionStatus), &nodeStatus); err == nil {
			for nodeIP, provision := range nodeStatus {
				if provision.IsHead {
					return nodeIP
				}
			}
		}
	}

	if c.Status.DashboardURL == "" {
		return ""
	}

	parsed, err := url.Parse(c.Status.DashboardURL)
	if err != nil {
		return ""
	}

	return parsed.Hostname()
}

func (controller *ClusterController) buildStaticNodeCluster(c *v1.Cluster) (*v1.StaticNodeCluster, error) {
	if c == nil || c.Metadata == nil {
		return nil, errors.New("cluster metadata is required")
	}

	if c.Spec == nil {
		return nil, errors.New("cluster spec is required")
	}

	sshConfig, err := util.ParseSSHClusterConfig(c)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse ssh cluster config")
	}

	if sshConfig.Provider.HeadIP == "" {
		return nil, errors.New("head IP can not be empty")
	}

	imageRegistry, err := controller.getUsedImageRegistry(c)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get used image registry")
	}

	imagePrefix, err := util.GetImagePrefix(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build image prefix")
	}

	headName := sshConfig.Provider.HeadIP
	nodes := []v1.StaticNodeClusterNodeSpec{
		{
			Name:    headName,
			IP:      sshConfig.Provider.HeadIP,
			Role:    v1.StaticNodeRoleHead,
			SSHAuth: copyStaticClusterAuth(&sshConfig.Auth),
		},
	}

	for _, workerIP := range sshConfig.Provider.WorkerIPs {
		if workerIP == "" {
			continue
		}

		nodes = append(nodes, v1.StaticNodeClusterNodeSpec{
			Name:    workerIP,
			IP:      workerIP,
			Role:    v1.StaticNodeRoleWorker,
			SSHAuth: copyStaticClusterAuth(&sshConfig.Auth),
		})
	}

	return &v1.StaticNodeCluster{
		APIVersion: "v1",
		Kind:       "StaticNodeCluster",
		Metadata: &v1.Metadata{
			Name:        c.Metadata.Name,
			Workspace:   c.Metadata.Workspace,
			Labels:      copyStaticClusterStringMap(c.Metadata.Labels),
			Annotations: copyStaticClusterStringMap(c.Metadata.Annotations),
		},
		Spec: &v1.StaticNodeClusterSpec{
			Version:         c.Spec.Version,
			ImageRegistry:   imagePrefix,
			Nodes:           nodes,
			UpgradeStrategy: v1.DefaultClusterUpgradeStrategy(),
		},
	}, nil
}

func (controller *ClusterController) findStaticNodeCluster(
	workspace string,
	name string,
) (*v1.StaticNodeCluster, bool, error) {
	filters := []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, name)},
	}
	if workspace != "" {
		filters = append(filters, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, workspace),
		})
	}

	clusters, err := controller.storage.ListStaticNodeCluster(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, false, err
	}

	if len(clusters) == 0 {
		return nil, false, nil
	}

	return &clusters[0], true, nil
}

func (controller *ClusterController) copyStaticNodeClusterStatus(
	c *v1.Cluster,
	desired *v1.StaticNodeCluster,
	status *v1.StaticNodeClusterStatus,
) {
	if c.Status == nil {
		c.Status = &v1.ClusterStatus{}
	}

	c.Status.DashboardURL = staticNodeClusterDashboardURL(desired)
	c.Status.DesiredNodes = len(desired.Spec.Nodes)

	if status != nil {
		c.Status.ReadyNodes = status.ReadyNodes
		if status.DesiredNodes > 0 {
			c.Status.DesiredNodes = status.DesiredNodes
		}
	}

	if status != nil && status.Phase == v1.StaticNodeClusterPhaseReady {
		c.Status.Initialized = true
		c.Status.Version = c.GetVersion()

		resources, err := controller.calculateStaticNodeClusterResources(desired)
		if err != nil {
			klog.Warningf("failed to calculate static cluster resources for %s: %v", c.Metadata.WorkspaceName(), err)
		} else {
			c.Status.ResourceInfo = resources
		}
	}
}

type clusterPhaseOverrideError struct {
	err   error
	phase v1.ClusterPhase
}

func (e *clusterPhaseOverrideError) Error() string {
	return e.err.Error()
}

func (e *clusterPhaseOverrideError) Unwrap() error {
	return e.err
}

func (e *clusterPhaseOverrideError) ClusterPhase() v1.ClusterPhase {
	return e.phase
}

func withClusterPhaseOverride(err error, phase v1.ClusterPhase) error {
	if err == nil || !isClusterOverridePhase(phase) {
		return err
	}

	return &clusterPhaseOverrideError{err: err, phase: phase}
}

func clusterPhaseOverrideFromError(err error) (v1.ClusterPhase, bool) {
	if err == nil {
		return "", false
	}

	var phaseErr interface {
		ClusterPhase() v1.ClusterPhase
	}

	if !errors.As(err, &phaseErr) {
		return "", false
	}

	phase := phaseErr.ClusterPhase()
	if !isClusterOverridePhase(phase) {
		return "", false
	}

	return phase, true
}

func isClusterOverridePhase(phase v1.ClusterPhase) bool {
	switch phase {
	case v1.ClusterPhaseInitializing, v1.ClusterPhaseUpdating, v1.ClusterPhaseUpgrading, v1.ClusterPhaseFailed:
		return true
	default:
		return false
	}
}

func staticNodeClusterProgressPhase(
	c *v1.Cluster,
	status *v1.StaticNodeClusterStatus,
) v1.ClusterPhase {
	if c == nil || !c.IsInitialized() {
		return v1.ClusterPhaseInitializing
	}

	if status != nil && (status.Phase == v1.StaticNodeClusterPhaseFailed || status.Phase == v1.StaticNodeClusterPhaseDegraded) {
		return v1.ClusterPhaseFailed
	}

	if staticNodeClusterStatusIsUpgrade(c, status) {
		return v1.ClusterPhaseUpgrading
	}

	return v1.ClusterPhaseUpdating
}

func staticNodeClusterStatusIsUpgrade(
	c *v1.Cluster,
	status *v1.StaticNodeClusterStatus,
) bool {
	if c == nil {
		return false
	}

	desiredVersion := c.GetVersion()
	if desiredVersion == "" {
		return false
	}

	if c.Status != nil && c.Status.Version != "" && c.Status.Version != desiredVersion {
		return true
	}

	if status == nil {
		return false
	}

	return status.Phase == v1.StaticNodeClusterPhaseUpgrading || status.Version != "" && status.Version != desiredVersion
}

func (controller *ClusterController) calculateStaticNodeClusterResources(
	staticCluster *v1.StaticNodeCluster,
) (*v1.ClusterResources, error) {
	var resourceParsers map[string]resourceparser.ResourceParser
	if controller.acceleratorManager != nil {
		resourceParsers = controller.acceleratorManager.GetAllParsers()
	}

	resourceClient := resourceview.NewRayResourceClient(
		dashboard.NewDashboardService(staticNodeClusterDashboardURL(staticCluster)),
		resourceParsers,
	)
	resourceBuilder := resourceview.NewResourceViewBuilder(resourceClient)

	resources, err := resourceBuilder.BuildClusterResources(context.Background(), nil)
	if err != nil {
		return nil, err
	}

	if err := controller.enrichStaticNodeClusterResources(staticCluster, resources); err != nil {
		return nil, err
	}

	return resources, nil
}

func (controller *ClusterController) enrichStaticNodeClusterResources(
	staticCluster *v1.StaticNodeCluster,
	resources *v1.ClusterResources,
) error {
	normalizeStaticNodeClusterResourceProducts(resources)

	if controller.storage == nil || staticCluster == nil || staticCluster.Metadata == nil {
		return nil
	}

	nodes, err := controller.listStaticNodesForResourceInfo(staticCluster)
	if err != nil {
		return err
	}

	enrichStaticNodeClusterDeviceResources(resources, nodes)
	normalizeStaticNodeClusterResourceProducts(resources)

	return nil
}

func (controller *ClusterController) listStaticNodesForResourceInfo(
	staticCluster *v1.StaticNodeCluster,
) ([]v1.StaticNode, error) {
	filters := []storage.Filter{
		{Column: "spec->>cluster", Operator: "eq", Value: staticCluster.Metadata.Name},
	}
	if staticCluster.Metadata.Workspace != "" {
		filters = append(filters, storage.Filter{
			Column:   "metadata->>workspace",
			Operator: "eq",
			Value:    staticCluster.Metadata.Workspace,
		})
	}

	nodes := []v1.StaticNode{}
	if err := controller.storage.GenericQuery(storage.STATIC_NODE_TABLE, "*", filters, &nodes); err != nil {
		return nil, errors.Wrapf(err, "failed to query static nodes for cluster %s resources", staticCluster.Metadata.WorkspaceName())
	}

	return nodes, nil
}

func normalizeStaticNodeClusterResourceProducts(resources *v1.ClusterResources) {
	if resources == nil {
		return
	}

	normalizeStaticNodeResourceInfoProducts(resources.Allocatable)
	normalizeStaticNodeResourceInfoProducts(resources.Available)

	for _, nodeResource := range resources.NodeResources {
		if nodeResource == nil {
			continue
		}

		normalizeStaticNodeResourceInfoProducts(nodeResource.Allocatable)
		normalizeStaticNodeResourceInfoProducts(nodeResource.Available)
	}
}

func normalizeStaticNodeResourceInfoProducts(info *v1.ResourceInfo) {
	if info == nil {
		return
	}

	for _, group := range info.AcceleratorGroups {
		if group == nil || len(group.ProductGroups) == 0 {
			continue
		}

		if group.Products == nil {
			group.Products = make(map[v1.AcceleratorProduct]*v1.AcceleratorProductResource)
		}

		for product, quantity := range group.ProductGroups {
			productResource := group.Products[product]
			if productResource == nil {
				productResource = &v1.AcceleratorProductResource{}
				group.Products[product] = productResource
			}

			if productResource.Quantity == 0 {
				productResource.Quantity = quantity
			}
		}
	}
}

func enrichStaticNodeClusterDeviceResources(
	resources *v1.ClusterResources,
	nodes []v1.StaticNode,
) {
	if resources == nil {
		return
	}

	for _, node := range nodes {
		if node.Status == nil || node.Status.Accelerator == nil || len(node.Status.Accelerator.Devices) == 0 {
			continue
		}

		acceleratorType := v1.AcceleratorType(node.Status.Accelerator.Type)
		if acceleratorType == "" || node.Status.Accelerator.Type == v1.StaticNodeAcceleratorTypeCPU {
			continue
		}

		nodeID := staticNodeResourceID(node)
		if nodeID == "" {
			continue
		}

		if resources.NodeResources == nil {
			resources.NodeResources = make(map[string]*v1.NodeResourceStatus)
		}

		nodeResource := resources.NodeResources[nodeID]
		if nodeResource == nil {
			nodeResource = &v1.NodeResourceStatus{}
			resources.NodeResources[nodeID] = nodeResource
		}

		devices := staticNodeClusterDeviceResources(nodeResource, *node.Status.Accelerator)
		if len(devices) == 0 {
			continue
		}

		nodeResource.Devices = devices
		enrichStaticNodeClusterAcceleratorMetadata(resources, acceleratorType, devices)
	}
}

func staticNodeResourceID(node v1.StaticNode) string {
	if node.Spec != nil && node.Spec.IP != "" {
		return node.Spec.IP
	}

	if node.Metadata != nil {
		return node.Metadata.Name
	}

	return ""
}

func staticNodeClusterDeviceResources(
	nodeResource *v1.NodeResourceStatus,
	accelerator v1.StaticNodeAcceleratorStatus,
) []*v1.DeviceResource {
	acceleratorType := v1.AcceleratorType(accelerator.Type)
	devices := make([]*v1.DeviceResource, 0, len(accelerator.Devices))

	for _, device := range accelerator.Devices {
		if device.UUID == "" {
			continue
		}

		allocatable := &v1.DeviceResourcePool{
			MemoryMiB: device.MemoryMiB,
			CoreUnits: 100,
		}
		devices = append(devices, &v1.DeviceResource{
			UUID:    device.UUID,
			Product: staticNodeDeviceProduct(nodeResource, acceleratorType, accelerator, device),
			Health:  device.Healthy,
			Allocatable: &v1.DeviceResourcePool{
				MemoryMiB: allocatable.MemoryMiB,
				CoreUnits: allocatable.CoreUnits,
			},
		})
	}

	sort.Slice(devices, func(i, j int) bool {
		return devices[i].UUID < devices[j].UUID
	})

	return devices
}

func staticNodeDeviceProduct(
	nodeResource *v1.NodeResourceStatus,
	acceleratorType v1.AcceleratorType,
	accelerator v1.StaticNodeAcceleratorStatus,
	device v1.StaticNodeAcceleratorDeviceStatus,
) string {
	deviceProduct := firstNonEmptyString(device.ProductModel, device.ProductName, accelerator.ProductModel, accelerator.ProductName)
	if product := staticNodeResourceProduct(nodeResource, acceleratorType, deviceProduct); product != "" {
		return string(product)
	}

	if deviceProduct != "" {
		return deviceProduct
	}

	return "unknown"
}

func staticNodeResourceProduct(
	nodeResource *v1.NodeResourceStatus,
	acceleratorType v1.AcceleratorType,
	fallbackProduct string,
) v1.AcceleratorProduct {
	if nodeResource == nil {
		return ""
	}

	if product := staticNodeResourceInfoProduct(nodeResource.Allocatable, acceleratorType, fallbackProduct); product != "" {
		return product
	}

	return staticNodeResourceInfoProduct(nodeResource.Available, acceleratorType, fallbackProduct)
}

func staticNodeResourceInfoProduct(
	info *v1.ResourceInfo,
	acceleratorType v1.AcceleratorType,
	fallbackProduct string,
) v1.AcceleratorProduct {
	if info == nil || info.AcceleratorGroups == nil {
		return ""
	}

	group := info.AcceleratorGroups[acceleratorType]
	if group == nil {
		return ""
	}

	fallback := v1.AcceleratorProduct(fallbackProduct)
	if fallbackProduct != "" {
		if _, ok := group.ProductGroups[fallback]; ok {
			return fallback
		}

		if _, ok := group.Products[fallback]; ok {
			return fallback
		}
	}

	if len(group.ProductGroups) == 1 {
		for product := range group.ProductGroups {
			return product
		}
	}

	if len(group.Products) == 1 {
		for product := range group.Products {
			return product
		}
	}

	return ""
}

func enrichStaticNodeClusterAcceleratorMetadata(
	resources *v1.ClusterResources,
	acceleratorType v1.AcceleratorType,
	devices []*v1.DeviceResource,
) {
	if resources.AcceleratorMetadata == nil {
		resources.AcceleratorMetadata = make(map[v1.AcceleratorType]*v1.AcceleratorMetadata)
	}

	metadata := resources.AcceleratorMetadata[acceleratorType]
	if metadata == nil {
		metadata = &v1.AcceleratorMetadata{Products: make(map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata)}
		resources.AcceleratorMetadata[acceleratorType] = metadata
	}

	if metadata.Products == nil {
		metadata.Products = make(map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata)
	}

	for _, device := range devices {
		if device == nil || device.Product == "" || device.Allocatable == nil || device.Allocatable.MemoryMiB <= 0 {
			continue
		}

		product := v1.AcceleratorProduct(device.Product)

		productMetadata := metadata.Products[product]
		if productMetadata == nil {
			metadata.Products[product] = &v1.AcceleratorProductMetadata{
				MemoryTotalMiB: float64(device.Allocatable.MemoryMiB),
			}

			continue
		}

		if productMetadata.MemoryTotalMiB == 0 {
			productMetadata.MemoryTotalMiB = float64(device.Allocatable.MemoryMiB)
		}
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

func (controller *ClusterController) getUsedImageRegistry(c *v1.Cluster) (*v1.ImageRegistry, error) {
	filters := []storage.Filter{
		{
			Column:   "metadata->name",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, c.Spec.ImageRegistry),
		},
	}

	if c.Metadata.Workspace != "" {
		filters = append(filters, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, c.Metadata.Workspace),
		})
	}

	imageRegistries, err := controller.storage.ListImageRegistry(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list image registry")
	}

	if len(imageRegistries) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	imageRegistry := &imageRegistries[0]
	if imageRegistry.Status == nil || imageRegistry.Status.Phase != v1.ImageRegistryPhaseCONNECTED {
		return nil, errors.New("image registry " + c.Spec.ImageRegistry + " not ready")
	}

	return imageRegistry, nil
}

func staticNodeClusterDashboardURL(staticCluster *v1.StaticNodeCluster) string {
	return fmt.Sprintf("http://%s:%d", staticNodeClusterHeadIP(staticCluster), staticNodeClusterDashboardPort)
}

func staticNodeClusterHeadIP(staticCluster *v1.StaticNodeCluster) string {
	if staticCluster == nil || staticCluster.Spec == nil {
		return ""
	}

	for _, node := range staticCluster.Spec.Nodes {
		if node.Role == v1.StaticNodeRoleHead {
			return node.IP
		}
	}

	return ""
}

func staticNodeClusterNotReadyError(staticCluster *v1.StaticNodeCluster) error {
	if staticCluster == nil || staticCluster.Status == nil {
		return errors.New("static node cluster is not ready")
	}

	if staticCluster.Status.ErrorMessage != "" {
		return errors.Errorf("static node cluster %s is not ready: %s", staticCluster.Metadata.Name, staticCluster.Status.ErrorMessage)
	}

	return errors.Errorf("static node cluster %s is not ready: phase=%s", staticCluster.Metadata.Name, staticCluster.Status.Phase)
}

func copyStaticClusterAuth(auth *v1.Auth) *v1.Auth {
	if auth == nil {
		return nil
	}

	copied := *auth

	return &copied
}

func copyStaticClusterStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}

	return copied
}
