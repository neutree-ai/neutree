package resource

import (
	"sort"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func EnrichStaticNodeClusterResources(resources *v1.ClusterResources, nodes []v1.StaticNode) {
	normalizeStaticNodeClusterResourceProducts(resources)
	enrichStaticNodeClusterDeviceResources(resources, nodes)
	normalizeStaticNodeClusterResourceProducts(resources)
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
