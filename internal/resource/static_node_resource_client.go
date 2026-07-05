package resource

import (
	"context"
	"errors"
	"sort"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var ErrIncompleteStaticNodeDeviceSnapshots = errors.New("incomplete static node device snapshots")

type StaticNodeResourceClient struct {
	nodeLister storage.StaticNodeLister
	baseClient ResourceClient
}

func NewStaticNodeClusterResourceClient(
	nodeLister storage.StaticNodeLister,
	baseClient ResourceClient,
) *StaticNodeResourceClient {
	return &StaticNodeResourceClient{
		nodeLister: nodeLister,
		baseClient: baseClient,
	}
}

func (c *StaticNodeResourceClient) ListNodes(ctx context.Context, cluster *v1.Cluster) ([]ResourceNode, error) {
	if c == nil {
		return nil, ErrIncompleteStaticNodeDeviceSnapshots
	}

	nodes, err := c.staticNodes(cluster)
	if err != nil {
		return nil, err
	}

	baseNodeList, baseNodes, err := c.baseResourceNodes(ctx, cluster)
	if err != nil {
		return nil, err
	}

	if len(nodes) == 0 {
		return baseNodeList, nil
	}

	result := make([]ResourceNode, 0, len(nodes))

	for _, node := range nodes {
		resourceNode, ok := resourceNodeFromStaticNodeDeviceSnapshot(node, baseResourceNode(node, baseNodes))
		if !ok {
			continue
		}

		result = append(result, resourceNode)
	}

	if len(result) != len(nodes) {
		return nil, ErrIncompleteStaticNodeDeviceSnapshots
	}

	return result, nil
}

func (c *StaticNodeResourceClient) ListEndpointInstances(
	_ context.Context,
	cluster *v1.Cluster,
	endpoint *v1.Endpoint,
) ([]EndpointInstanceResource, error) {
	if c == nil {
		return nil, ErrIncompleteStaticNodeDeviceSnapshots
	}

	nodes, err := c.staticNodes(cluster)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, nil
	}

	instances := make([]EndpointInstanceResource, 0)
	product := endpointAcceleratorProduct(endpoint)
	for _, node := range nodes {
		if node == nil || node.Status == nil {
			continue
		}

		for _, allocation := range node.Status.Allocations {
			if !staticNodeAllocationMatchesEndpoint(allocation, endpoint) {
				continue
			}

			devices := staticNodeEndpointAllocationDevices(allocation.Devices, staticNodeResourceKey(node), product)
			if len(devices) == 0 {
				continue
			}

			instances = append(instances, EndpointInstanceResource{
				InstanceID: allocation.ReplicaID,
				ReplicaID:  allocation.ReplicaID,
				NodeID:     staticNodeResourceKey(node),
				Devices:    devices,
			})
		}
	}

	if len(instances) == 0 {
		return nil, nil
	}

	sortEndpointInstanceResources(instances)

	return instances, nil
}

func (c *StaticNodeResourceClient) staticNodes(cluster *v1.Cluster) ([]*v1.StaticNode, error) {
	if c == nil || c.nodeLister == nil || cluster == nil || cluster.Metadata == nil {
		return nil, ErrIncompleteStaticNodeDeviceSnapshots
	}

	workspace := cluster.Metadata.Workspace
	clusterName := cluster.Metadata.Name
	items, err := c.nodeLister.ListStaticNode(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
			{Column: "spec->>cluster", Operator: "eq", Value: clusterName},
		},
	})
	if err != nil {
		return nil, err
	}

	nodes := make([]*v1.StaticNode, 0, len(items))
	for i := range items {
		node := &items[i]
		if node.Spec == nil || node.Metadata == nil {
			continue
		}
		if node.Metadata.Workspace != workspace || node.Spec.Cluster != clusterName {
			continue
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

func (c *StaticNodeResourceClient) baseResourceNodes(ctx context.Context, cluster *v1.Cluster) ([]ResourceNode, map[string]ResourceNode, error) {
	if c == nil || c.baseClient == nil {
		return nil, nil, nil
	}

	nodes, err := c.baseClient.ListNodes(ctx, cluster)
	if err != nil {
		return nil, nil, err
	}

	return nodes, staticNodeBaseResourceNodes(nodes), nil
}

func baseResourceNode(node *v1.StaticNode, baseNodes map[string]ResourceNode) *ResourceNode {
	if len(baseNodes) == 0 || node == nil {
		return nil
	}

	key := staticNodeResourceKey(node)
	if key == "" {
		return nil
	}
	if base, ok := baseNodes[key]; ok {
		return &base
	}

	return nil
}

func resourceNodeFromStaticNodeDeviceSnapshot(node *v1.StaticNode, base *ResourceNode) (ResourceNode, bool) {
	if node == nil || node.Status == nil || node.Status.Accelerator == nil {
		return ResourceNode{}, false
	}

	acceleratorType := v1.AcceleratorType(node.Status.Accelerator.Type)
	if acceleratorType == "" {
		return ResourceNode{}, false
	}
	if acceleratorType == v1.AcceleratorType(v1.StaticNodeAcceleratorTypeCPU) ||
		len(node.Status.Accelerator.Devices) == 0 {
		return staticNodeBaseResourceNode(node, base)
	}

	nodeStatus := &v1.NodeResourceStatus{
		ResourceStatus: v1.ResourceStatus{
			Allocatable: &v1.ResourceInfo{
				AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
			},
			Available: &v1.ResourceInfo{
				AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
			},
		},
		Devices: make([]*v1.DeviceResource, 0, len(node.Status.Accelerator.Devices)),
	}
	completeStaticNodeCPUAndMemory(nodeStatus, base)
	metadata := make(map[v1.AcceleratorType]*v1.AcceleratorMetadata)
	allocationsByUUID := staticNodeAllocationsByUUID(node.Status.Allocations)
	ordersByUUID := staticNodeDeviceOrders(node.Status.Accelerator.Devices)
	baseProduct := staticNodeBaseAcceleratorProduct(base, acceleratorType)
	if baseProduct == "" {
		return ResourceNode{}, false
	}

	for _, device := range node.Status.Accelerator.Devices {
		if device.UUID == "" {
			continue
		}

		allocation := allocationsByUUID[device.UUID]
		allocatablePool := staticNodeDeviceAllocatablePool(device)
		availablePool := staticNodeDeviceAvailablePool(device, allocation)
		if !device.Healthy {
			allocatablePool = &v1.DeviceResourcePool{}
			availablePool = &v1.DeviceResourcePool{}
		}

		nodeStatus.Devices = append(nodeStatus.Devices, &v1.DeviceResource{
			UUID:        device.UUID,
			Product:     baseProduct,
			Health:      device.Healthy,
			MinorNumber: ordersByUUID[device.UUID].MinorNumber,
			Order:       ordersByUUID[device.UUID].Order,
			Allocatable: allocatablePool,
			Available:   availablePool,
		})
		addStaticNodeAcceleratorMetadata(metadata, acceleratorType, baseProduct, device.MemoryMiB)

		if device.Healthy {
			addStaticNodeAcceleratorResource(nodeStatus.Allocatable, acceleratorType, baseProduct, 1, allocatablePool)
		}

		if device.Healthy && hasDeviceAvailableCapacity(availablePool) {
			addStaticNodeAcceleratorResource(nodeStatus.Available, acceleratorType, baseProduct, 1, availablePool)
		}
	}

	if len(nodeStatus.Devices) == 0 {
		return ResourceNode{}, false
	}

	return ResourceNode{
		ID:                  staticNodeResourceKey(node),
		Status:              nodeStatus,
		AcceleratorMetadata: metadata,
	}, true
}

func staticNodeBaseResourceNode(node *v1.StaticNode, base *ResourceNode) (ResourceNode, bool) {
	if base == nil || base.Status == nil {
		return ResourceNode{}, false
	}

	resourceNode := *base
	if resourceNode.ID == "" {
		resourceNode.ID = staticNodeResourceKey(node)
	}

	status := *base.Status
	resourceNode.Status = &status
	if resourceNode.Status.Devices == nil {
		resourceNode.Status.Devices = []*v1.DeviceResource{}
	}

	return resourceNode, true
}

func staticNodeBaseResourceNodes(nodes []ResourceNode) map[string]ResourceNode {
	result := map[string]ResourceNode{}
	for _, node := range nodes {
		if node.ID == "" {
			continue
		}

		result[node.ID] = node
	}

	return result
}

func completeStaticNodeCPUAndMemory(status *v1.NodeResourceStatus, base *ResourceNode) {
	if status == nil || base == nil || base.Status == nil {
		return
	}

	if status.Allocatable != nil && base.Status.Allocatable != nil {
		status.Allocatable.CPU = base.Status.Allocatable.CPU
		status.Allocatable.Memory = base.Status.Allocatable.Memory
	}
	if status.Available != nil && base.Status.Available != nil {
		status.Available.CPU = base.Status.Available.CPU
		status.Available.Memory = base.Status.Available.Memory
	}
}

func staticNodeAllocationMatchesEndpoint(
	allocation v1.StaticNodeAllocationStatus,
	endpoint *v1.Endpoint,
) bool {
	if allocation.Endpoint != endpoint.Metadata.Name {
		return false
	}
	if allocation.Workspace != endpoint.Metadata.Workspace {
		return false
	}

	return true
}

func endpointAcceleratorProduct(endpoint *v1.Endpoint) string {
	if endpoint == nil || endpoint.Spec == nil || endpoint.Spec.Resources == nil {
		return ""
	}

	return endpoint.Spec.Resources.GetAcceleratorProduct()
}

func staticNodeEndpointAllocationDevices(
	devices []v1.DeviceAllocation,
	nodeID string,
	product string,
) []v1.DeviceAllocation {
	result := make([]v1.DeviceAllocation, 0, len(devices))
	for _, device := range devices {
		if device.UUID == "" {
			continue
		}

		copied := device
		if copied.NodeID == "" {
			copied.NodeID = nodeID
		}
		// Endpoint resources report the product requested by the endpoint spec,
		// not the product guessed from node-agent allocation details.
		copied.Product = product
		result = append(result, copied)
	}

	return result
}

type staticNodeDeviceOrder struct {
	MinorNumber *int
	Order       *int
}

func staticNodeDeviceOrders(devices []v1.StaticNodeAcceleratorDeviceStatus) map[string]staticNodeDeviceOrder {
	devicesWithMinor := make([]v1.StaticNodeAcceleratorDeviceStatus, 0, len(devices))
	for _, device := range devices {
		if device.UUID == "" || device.MinorNumber < 0 {
			continue
		}

		devicesWithMinor = append(devicesWithMinor, device)
	}

	sort.SliceStable(devicesWithMinor, func(i, j int) bool {
		return devicesWithMinor[i].MinorNumber < devicesWithMinor[j].MinorNumber
	})

	result := make(map[string]staticNodeDeviceOrder, len(devicesWithMinor))
	for order, device := range devicesWithMinor {
		minorNumber := device.MinorNumber
		displayOrder := order
		result[device.UUID] = staticNodeDeviceOrder{
			MinorNumber: &minorNumber,
			Order:       &displayOrder,
		}
	}

	return result
}

func staticNodeAllocationsByUUID(allocations []v1.StaticNodeAllocationStatus) map[string]*v1.DeviceAllocation {
	result := map[string]*v1.DeviceAllocation{}
	for i := range allocations {
		for j := range allocations[i].Devices {
			device := &allocations[i].Devices[j]
			if device.UUID == "" {
				continue
			}

			existing := result[device.UUID]
			if existing == nil {
				copied := *device
				result[device.UUID] = &copied
				continue
			}

			existing.MemoryMiB += device.MemoryMiB
			existing.CoreUnits += device.CoreUnits
		}
	}

	return result
}

func staticNodeBaseAcceleratorProduct(base *ResourceNode, acceleratorType v1.AcceleratorType) string {
	if base == nil || base.Status == nil {
		return ""
	}

	products := map[string]struct{}{}
	for _, info := range []*v1.ResourceInfo{base.Status.Allocatable, base.Status.Available} {
		if info == nil {
			continue
		}

		group := info.AcceleratorGroups[acceleratorType]
		if group == nil {
			continue
		}

		for product := range group.ProductGroups {
			if product != "" {
				products[string(product)] = struct{}{}
			}
		}
		for product := range group.Products {
			if product != "" {
				products[string(product)] = struct{}{}
			}
		}
	}

	if len(products) != 1 {
		return ""
	}

	for product := range products {
		return product
	}

	return ""
}

func staticNodeDeviceAllocatablePool(device v1.StaticNodeAcceleratorDeviceStatus) *v1.DeviceResourcePool {
	return &v1.DeviceResourcePool{
		MemoryMiB: device.MemoryMiB,
		CoreUnits: 100,
	}
}

func staticNodeDeviceAvailablePool(
	device v1.StaticNodeAcceleratorDeviceStatus,
	allocation *v1.DeviceAllocation,
) *v1.DeviceResourcePool {
	if allocation == nil {
		return staticNodeDeviceAllocatablePool(device)
	}

	if allocation.MemoryMiB == 0 && allocation.CoreUnits == 0 {
		return &v1.DeviceResourcePool{}
	}

	return &v1.DeviceResourcePool{
		MemoryMiB: maxInt64(device.MemoryMiB-allocation.MemoryMiB, 0),
		CoreUnits: maxInt64(100-allocation.CoreUnits, 0),
	}
}

func addStaticNodeAcceleratorResource(
	info *v1.ResourceInfo,
	acceleratorType v1.AcceleratorType,
	product string,
	quantity float64,
	pool *v1.DeviceResourcePool,
) {
	if info == nil || acceleratorType == "" || product == "" || quantity == 0 {
		return
	}

	if info.AcceleratorGroups == nil {
		info.AcceleratorGroups = make(map[v1.AcceleratorType]*v1.AcceleratorGroup)
	}

	group := info.AcceleratorGroups[acceleratorType]
	if group == nil {
		group = &v1.AcceleratorGroup{
			ProductGroups: make(map[v1.AcceleratorProduct]float64),
			Products:      make(map[v1.AcceleratorProduct]*v1.AcceleratorProductResource),
		}
		info.AcceleratorGroups[acceleratorType] = group
	}
	if group.ProductGroups == nil {
		group.ProductGroups = make(map[v1.AcceleratorProduct]float64)
	}
	if group.Products == nil {
		group.Products = make(map[v1.AcceleratorProduct]*v1.AcceleratorProductResource)
	}

	acceleratorProduct := v1.AcceleratorProduct(product)
	group.Quantity += quantity
	group.ProductGroups[acceleratorProduct] += quantity
	productResource := group.Products[acceleratorProduct]
	if productResource == nil {
		productResource = &v1.AcceleratorProductResource{}
		group.Products[acceleratorProduct] = productResource
	}
	productResource.Quantity += quantity

	if pool == nil {
		return
	}
	if productResource.Virtualization == nil {
		productResource.Virtualization = &v1.AcceleratorVirtualizationResource{}
	}
	productResource.Virtualization.MemoryMiB += float64(pool.MemoryMiB)
	productResource.Virtualization.CoreUnits += float64(pool.CoreUnits)
}

func addStaticNodeAcceleratorMetadata(
	metadata map[v1.AcceleratorType]*v1.AcceleratorMetadata,
	acceleratorType v1.AcceleratorType,
	product string,
	memoryMiB int64,
) {
	if metadata == nil || acceleratorType == "" || product == "" || memoryMiB <= 0 {
		return
	}

	acceleratorMetadata := metadata[acceleratorType]
	if acceleratorMetadata == nil {
		acceleratorMetadata = &v1.AcceleratorMetadata{
			Products: make(map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata),
		}
		metadata[acceleratorType] = acceleratorMetadata
	}
	if acceleratorMetadata.Products == nil {
		acceleratorMetadata.Products = make(map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata)
	}

	productMetadata := acceleratorMetadata.Products[v1.AcceleratorProduct(product)]
	if productMetadata == nil {
		productMetadata = &v1.AcceleratorProductMetadata{}
		acceleratorMetadata.Products[v1.AcceleratorProduct(product)] = productMetadata
	}
	if productMetadata.MemoryTotalMiB == 0 {
		productMetadata.MemoryTotalMiB = float64(memoryMiB)
	}
}

func staticNodeResourceKey(node *v1.StaticNode) string {
	if node != nil && node.Spec != nil && node.Spec.IP != "" {
		return node.Spec.IP
	}

	return ""
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}

	return b
}
