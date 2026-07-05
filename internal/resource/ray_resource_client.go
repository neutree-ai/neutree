package resource

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

type RayResourceClient struct {
	client  dashboard.DashboardService
	parsers map[string]resourceparser.ResourceParser
}

func NewRayResourceClient(dashboardClient dashboard.DashboardService, parsers map[string]resourceparser.ResourceParser) *RayResourceClient {
	return &RayResourceClient{
		client:  dashboardClient,
		parsers: parsers,
	}
}

func (c *RayResourceClient) ListNodes(_ context.Context, _ *v1.Cluster) ([]ResourceNode, error) {
	nodeList, err := c.client.ListNodes()
	if err != nil {
		return nil, fmt.Errorf("failed to get node list from Ray Dashboard: %w", err)
	}

	nodes := make([]ResourceNode, 0, len(nodeList))

	for _, node := range nodeList {
		if node.Raylet.State != v1.AliveNodeState {
			continue
		}

		availableResources := map[string]float64{}
		allocatableResources := map[string]float64{}

		for resourceKey, quantity := range node.Raylet.Resources {
			allocatableResources[resourceKey] = quantity
			availableResources[resourceKey] = quantity
		}

		for _, workers := range node.Raylet.CoreWorkersStats {
			for resourceKey, allocations := range workers.UsedResources {
				availableResources[resourceKey] -= float64(allocations.TotalAllocation())
			}
		}

		klog.V(4).Infof("Node %s allocatable resources: %+v", node.IP, allocatableResources)
		klog.V(4).Infof("Node %s available resources: %+v", node.IP, availableResources)

		resourceStatus, err := transformRayResources(availableResources, allocatableResources, c.parsers)
		if err != nil {
			return nil, fmt.Errorf("failed to transform resources for node %s: %w", node.IP, err)
		}

		nodes = append(nodes, resourceNodeFromStatus(node.IP, resourceStatus))
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].ID < nodes[j].ID
	})

	return nodes, nil
}

func (c *RayResourceClient) ListEndpointInstances(
	_ context.Context,
	_ *v1.Cluster,
	_ *v1.Endpoint,
) ([]EndpointInstanceResource, error) {
	return nil, nil
}

func transformRayResources(
	availableResources, allocatableResources map[string]float64,
	parsers map[string]resourceparser.ResourceParser,
) (*v1.ResourceStatus, error) {
	result := &v1.ResourceStatus{
		Allocatable: &v1.ResourceInfo{
			CPU:               nonNegativeRounded(allocatableResources["CPU"]),
			Memory:            memoryBytesToGiB(allocatableResources["memory"]),
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
		Available: &v1.ResourceInfo{
			CPU:               nonNegativeRounded(availableResources["CPU"]),
			Memory:            memoryBytesToGiB(availableResources["memory"]),
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
	}

	for resourceKey, parser := range parsers {
		allocatableInfo, err := parser.ParseFromRay(allocatableResources)
		if err != nil {
			return nil, fmt.Errorf("failed to parse allocatable resources from Ray for resource %s: %w", resourceKey, err)
		}

		if allocatableInfo != nil && len(allocatableInfo.AcceleratorGroups) > 0 {
			mergeAcceleratorGroups(result.Allocatable.AcceleratorGroups, allocatableInfo.AcceleratorGroups)
			mergeResourceInfoMetadata(result.Allocatable, allocatableInfo.AcceleratorMetadata)
		}

		availableInfo, err := parser.ParseFromRay(availableResources)
		if err != nil {
			return nil, fmt.Errorf("failed to parse available resources from Ray for resource %s: %w", resourceKey, err)
		}

		if availableInfo != nil && len(availableInfo.AcceleratorGroups) > 0 {
			mergeAcceleratorGroups(result.Available.AcceleratorGroups, availableInfo.AcceleratorGroups)
			mergeResourceInfoMetadata(result.Available, availableInfo.AcceleratorMetadata)
		}
	}

	return result, nil
}
