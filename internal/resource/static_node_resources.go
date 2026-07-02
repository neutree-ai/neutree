package resource

import (
	"context"
	"sort"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type StaticNodeClusterResourceClient struct {
	rayNodes    ResourceClient
	staticNodes storage.StaticNodeLister
	workspace   string
	clusterName string
}

func NewStaticNodeClusterResourceClient(
	rayNodes ResourceClient,
	staticNodes storage.StaticNodeLister,
	workspace string,
	clusterName string,
) (*StaticNodeClusterResourceClient, error) {
	if rayNodes == nil {
		return nil, errors.New("Ray resource client is required")
	}

	if staticNodes == nil {
		return nil, errors.New("static node lister is required")
	}

	return &StaticNodeClusterResourceClient{
		rayNodes:    rayNodes,
		staticNodes: staticNodes,
		workspace:   workspace,
		clusterName: clusterName,
	}, nil
}

func (c *StaticNodeClusterResourceClient) ListNodes(
	ctx context.Context,
	opts ListNodesOptions,
) ([]ResourceNode, error) {
	if c == nil {
		return nil, errors.New("Ray resource client is required")
	}

	nodes, err := c.rayNodes.ListNodes(ctx, opts)
	if err != nil {
		return nil, err
	}

	staticNodes, err := c.staticNodes.ListStaticNode(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: c.workspace},
			{Column: "spec->>cluster", Operator: "eq", Value: c.clusterName},
		},
	})
	if err != nil {
		return nil, err
	}

	return enrichStaticNodeClusterResourceNodes(nodes, staticNodePointers(staticNodes, c.workspace, c.clusterName)), nil
}

func (c *StaticNodeClusterResourceClient) ListEndpointInstances(
	ctx context.Context,
	opts ListEndpointInstancesOptions,
) ([]EndpointInstanceResource, error) {
	if c == nil {
		return nil, errors.New("Ray resource client is required")
	}

	return c.rayNodes.ListEndpointInstances(ctx, opts)
}

func enrichStaticNodeClusterResourceNodes(nodes []ResourceNode, staticNodes []*v1.StaticNode) []ResourceNode {
	enriched := append([]ResourceNode(nil), nodes...)
	normalizeStaticNodeResourceNodeProducts(enriched)
	enrichStaticNodeClusterResourceNodeDevices(&enriched, staticNodes)
	normalizeStaticNodeResourceNodeProducts(enriched)

	return enriched
}

func staticNodePointers(nodes []v1.StaticNode, workspace, clusterName string) []*v1.StaticNode {
	result := make([]*v1.StaticNode, 0, len(nodes))

	for i := range nodes {
		node := &nodes[i]
		if node.Spec == nil {
			continue
		}

		if node.Metadata.Workspace != workspace || node.Spec.Cluster != clusterName {
			continue
		}

		result = append(result, node)
	}

	return result
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

func normalizeStaticNodeResourceNodeProducts(nodes []ResourceNode) {
	for _, node := range nodes {
		if node.Status == nil {
			continue
		}

		normalizeStaticNodeResourceInfoProducts(node.Status.Allocatable)
		normalizeStaticNodeResourceInfoProducts(node.Status.Available)
	}
}

func enrichStaticNodeClusterResourceNodeDevices(
	nodes *[]ResourceNode,
	staticNodes []*v1.StaticNode,
) {
	byID := make(map[string]int, len(*nodes))
	for i, node := range *nodes {
		byID[node.ID] = i
	}

	for _, staticNode := range staticNodes {
		if staticNode == nil || staticNode.Status == nil || staticNode.Status.Accelerator == nil || len(staticNode.Status.Accelerator.Devices) == 0 {
			continue
		}

		acceleratorType := v1.AcceleratorType(staticNode.Status.Accelerator.Type)
		if acceleratorType == "" || staticNode.Status.Accelerator.Type == v1.StaticNodeAcceleratorTypeCPU {
			continue
		}

		nodeID := staticNodeResourceID(staticNode)
		index, ok := byID[nodeID]

		if !ok {
			*nodes = append(*nodes, ResourceNode{ID: nodeID})
			index = len(*nodes) - 1
			byID[nodeID] = index
		}

		node := &(*nodes)[index]
		if node.Status == nil {
			node.Status = &v1.NodeResourceStatus{}
		}

		devices := staticNodeClusterDeviceResources(node.Status, *staticNode.Status.Accelerator)
		if len(devices) == 0 {
			continue
		}

		node.Status.Devices = devices
		enrichStaticNodeClusterResourceNodeMetadata(node, acceleratorType, devices)
	}
}

func enrichStaticNodeClusterResourceNodeMetadata(
	node *ResourceNode,
	acceleratorType v1.AcceleratorType,
	devices []*v1.DeviceResource,
) {
	if node.AcceleratorMetadata == nil {
		node.AcceleratorMetadata = make(map[v1.AcceleratorType]*v1.AcceleratorMetadata)
	}

	metadata := &v1.ClusterResources{AcceleratorMetadata: node.AcceleratorMetadata}
	enrichStaticNodeClusterAcceleratorMetadata(metadata, acceleratorType, devices)
	node.AcceleratorMetadata = metadata.AcceleratorMetadata
}

func staticNodeResourceID(node *v1.StaticNode) string {
	if node == nil {
		return ""
	}

	if node.Spec != nil && node.Spec.IP != "" {
		return node.Spec.IP
	}

	return node.Metadata.Name
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
			Product: staticNodeDeviceProduct(nodeResource, acceleratorType, device),
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
	device v1.StaticNodeAcceleratorDeviceStatus,
) string {
	deviceProduct := firstNonEmptyString(device.ProductModel, device.ProductName)
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
