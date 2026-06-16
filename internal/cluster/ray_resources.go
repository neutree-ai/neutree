package cluster

import (
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

func calculateRayDashboardClusterResources(
	dashboardSvc dashboard.DashboardService,
	resourceParserMap map[string]plugin.ResourceParser,
) (*v1.ClusterResources, error) {
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
			if strings.HasPrefix(resourceKey, "node:") {
				continue
			}

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

		nodeResourceStatus, err := transformRayResources(nodeAvailableResource, nodeAllocatableResource, resourceParserMap)
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

	clusterResourceStatus, err := transformRayResources(availableResource, allocatableResource, resourceParserMap)
	if err != nil {
		return nil, fmt.Errorf("failed to transform cluster resources: %w", err)
	}

	return &v1.ClusterResources{
		ResourceStatus: *clusterResourceStatus,
		NodeResources:  nodeResources,
	}, nil
}

func CalculateRayDashboardClusterResources(
	dashboardSvc dashboard.DashboardService,
	resourceParserMap map[string]plugin.ResourceParser,
) (*v1.ClusterResources, error) {
	return calculateRayDashboardClusterResources(dashboardSvc, resourceParserMap)
}

func transformRayResources(
	availableResource map[string]float64,
	allocatableResource map[string]float64,
	resourceParserMap map[string]plugin.ResourceParser,
) (*v1.ResourceStatus, error) {
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

	for resourceKey, parser := range resourceParserMap {
		allocatableInfo, err := parser.ParseFromRay(allocatableResource)
		if err != nil {
			return nil, fmt.Errorf("failed to parse allocatable resources from Ray for resource %s: %w", resourceKey, err)
		}

		mergeAcceleratorGroups(result.Allocatable.AcceleratorGroups, allocatableInfo)

		availableInfo, err := parser.ParseFromRay(availableResource)
		if err != nil {
			return nil, fmt.Errorf("failed to parse available resources from Ray for resource %s: %w", resourceKey, err)
		}

		mergeAcceleratorGroups(result.Available.AcceleratorGroups, availableInfo)
	}

	return result, nil
}

func mergeAcceleratorGroups(
	target map[v1.AcceleratorType]*v1.AcceleratorGroup,
	resourceInfo *v1.ResourceInfo,
) {
	if resourceInfo == nil || len(resourceInfo.AcceleratorGroups) == 0 {
		return
	}

	for key, group := range resourceInfo.AcceleratorGroups {
		if existingGroup, exists := target[key]; exists {
			existingGroup.Quantity += group.Quantity
			for productKey, quantity := range group.ProductGroups {
				if existingQuantity, exists := existingGroup.ProductGroups[productKey]; exists {
					existingGroup.ProductGroups[productKey] = existingQuantity + quantity
				} else {
					existingGroup.ProductGroups[productKey] = quantity
				}
			}

			target[key] = existingGroup
		} else {
			target[key] = group
		}
	}
}
