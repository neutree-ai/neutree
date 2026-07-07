package resource

import (
	"context"
	"math"
	"sort"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type ResourceClient interface {
	ListNodes(ctx context.Context, cluster *v1.Cluster) ([]ResourceNode, error)
	ListEndpointInstances(
		ctx context.Context,
		cluster *v1.Cluster,
		endpoint *v1.Endpoint,
	) ([]EndpointInstanceResource, error)
}

const BytesPerGiB = 1024 * 1024 * 1024

type ResourceNode struct {
	ID                  string
	Status              *v1.NodeResourceStatus
	AcceleratorMetadata map[v1.AcceleratorType]*v1.AcceleratorMetadata
}

type EndpointInstanceResource struct {
	InstanceID string
	ReplicaID  string
	NodeID     string
	Devices    []v1.DeviceAllocation
}

func resourceNodeFromStatus(nodeID string, status *v1.ResourceStatus) ResourceNode {
	metadata := detachAcceleratorMetadata(status)

	return ResourceNode{
		ID: nodeID,
		Status: &v1.NodeResourceStatus{
			ResourceStatus: *status,
		},
		AcceleratorMetadata: metadata,
	}
}

func hasDeviceAvailableCapacity(pool *v1.DeviceResourcePool) bool {
	return pool != nil && pool.MemoryMiB > 0 && pool.CoreUnits > 0
}

func memoryBytesToGiB(value float64) float64 {
	if value < 0 {
		return 0
	}

	return roundFloat64ToTwoDecimals(value / BytesPerGiB)
}

func nonNegativeRounded(value float64) float64 {
	if value < 0 {
		return 0
	}

	return roundFloat64ToTwoDecimals(value)
}

func roundFloat64ToTwoDecimals(input float64) float64 {
	return math.Round(input*100) / 100
}

func sortEndpointInstanceResources(instances []EndpointInstanceResource) {
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].NodeID != instances[j].NodeID {
			return instances[i].NodeID < instances[j].NodeID
		}

		if instances[i].InstanceID != instances[j].InstanceID {
			return instances[i].InstanceID < instances[j].InstanceID
		}

		return instances[i].ReplicaID < instances[j].ReplicaID
	})

	for i := range instances {
		sort.Slice(instances[i].Devices, func(j, k int) bool {
			if instances[i].Devices[j].NodeID != instances[i].Devices[k].NodeID {
				return instances[i].Devices[j].NodeID < instances[i].Devices[k].NodeID
			}

			return instances[i].Devices[j].UUID < instances[i].Devices[k].UUID
		})
	}
}

func mergeResourceInfoMetadata(
	info *v1.ResourceInfo,
	metadata map[v1.AcceleratorType]*v1.AcceleratorMetadata,
) {
	if info == nil || len(metadata) == 0 {
		return
	}

	if info.AcceleratorMetadata == nil {
		info.AcceleratorMetadata = make(map[v1.AcceleratorType]*v1.AcceleratorMetadata)
	}

	mergeAcceleratorMetadata(info.AcceleratorMetadata, metadata)
}

func detachAcceleratorMetadata(status *v1.ResourceStatus) map[v1.AcceleratorType]*v1.AcceleratorMetadata {
	metadata := make(map[v1.AcceleratorType]*v1.AcceleratorMetadata)
	if status == nil {
		return metadata
	}
	// Product metadata describes the accelerator type, not node capacity. Keep
	// node resources focused on Used/Total and aggregate metadata at cluster
	// level to avoid repeating it under every node.
	if status.Allocatable != nil {
		mergeAcceleratorMetadata(metadata, status.Allocatable.AcceleratorMetadata)
		status.Allocatable.AcceleratorMetadata = nil
	}

	if status.Available != nil {
		mergeAcceleratorMetadata(metadata, status.Available.AcceleratorMetadata)
		status.Available.AcceleratorMetadata = nil
	}

	return metadata
}

func mergeAcceleratorGroups(target, source map[v1.AcceleratorType]*v1.AcceleratorGroup) {
	for acceleratorType, sourceGroup := range source {
		if sourceGroup == nil {
			continue
		}

		targetGroup := target[acceleratorType]
		if targetGroup == nil {
			targetGroup = &v1.AcceleratorGroup{}
			target[acceleratorType] = targetGroup
		}

		targetGroup.Quantity += sourceGroup.Quantity

		if len(sourceGroup.ProductGroups) > 0 {
			if targetGroup.ProductGroups == nil {
				targetGroup.ProductGroups = make(map[v1.AcceleratorProduct]float64)
			}

			for productType, quantity := range sourceGroup.ProductGroups {
				targetGroup.ProductGroups[productType] += quantity
			}
		}

		if len(sourceGroup.Products) > 0 {
			if targetGroup.Products == nil {
				targetGroup.Products = make(map[v1.AcceleratorProduct]*v1.AcceleratorProductResource)
			}

			for productType, sourceProduct := range sourceGroup.Products {
				if sourceProduct == nil {
					continue
				}

				targetProduct := targetGroup.Products[productType]
				if targetProduct == nil {
					targetProduct = &v1.AcceleratorProductResource{}
					targetGroup.Products[productType] = targetProduct
				}

				targetProduct.Quantity += sourceProduct.Quantity

				if sourceProduct.Virtualization != nil {
					if targetProduct.Virtualization == nil {
						targetProduct.Virtualization = &v1.AcceleratorVirtualizationResource{}
					}

					targetProduct.Virtualization.MemoryMiB += sourceProduct.Virtualization.MemoryMiB
					targetProduct.Virtualization.CoreUnits += sourceProduct.Virtualization.CoreUnits
				}
			}
		}
	}
}

func mergeAcceleratorMetadata(target, source map[v1.AcceleratorType]*v1.AcceleratorMetadata) {
	for acceleratorType, sourceMetadata := range source {
		if sourceMetadata == nil || len(sourceMetadata.Products) == 0 {
			continue
		}

		targetMetadata := target[acceleratorType]
		if targetMetadata == nil {
			targetMetadata = &v1.AcceleratorMetadata{
				Products: make(map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata),
			}
			target[acceleratorType] = targetMetadata
		}

		if targetMetadata.Products == nil {
			targetMetadata.Products = make(map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata)
		}

		for product, sourceProduct := range sourceMetadata.Products {
			if sourceProduct == nil {
				continue
			}

			if _, exists := targetMetadata.Products[product]; !exists {
				targetMetadata.Products[product] = &v1.AcceleratorProductMetadata{
					MemoryTotalMiB: sourceProduct.MemoryTotalMiB,
				}
			}
		}
	}
}

func nonZeroInt64(value, fallback int64) int64 {
	if value == 0 {
		return fallback
	}

	return value
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}

	return value
}
