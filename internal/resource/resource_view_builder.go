package resource

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type ResourceViewBuilder interface {
	BuildClusterResources(ctx context.Context, cluster *v1.Cluster) (*v1.ClusterResources, error)
	BuildEndpointResources(
		ctx context.Context,
		cluster *v1.Cluster,
		endpoint *v1.Endpoint,
	) (*v1.EndpointResourceStatus, error)
}

type resourceViewBuilder struct {
	resourceClient ResourceClient
}

func NewResourceViewBuilder(resourceClient ResourceClient) ResourceViewBuilder {
	return &resourceViewBuilder{
		resourceClient: resourceClient,
	}
}

func (b *resourceViewBuilder) BuildClusterResources(
	ctx context.Context,
	cluster *v1.Cluster,
) (*v1.ClusterResources, error) {
	if b.resourceClient == nil {
		return nil, fmt.Errorf("resource client is nil")
	}

	nodes, err := b.resourceClient.ListNodes(ctx, cluster)
	if err != nil {
		return nil, err
	}

	return buildClusterResourcesFromResourceNodes(nodes), nil
}

func (b *resourceViewBuilder) BuildEndpointResources(
	ctx context.Context,
	cluster *v1.Cluster,
	endpoint *v1.Endpoint,
) (*v1.EndpointResourceStatus, error) {
	if b.resourceClient == nil {
		return nil, fmt.Errorf("resource client is nil")
	}

	instances, err := b.resourceClient.ListEndpointInstances(ctx, cluster, endpoint)
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, nil
	}

	return buildEndpointResourcesFromEndpointInstances(instances, clusterResourceInfo(cluster)), nil
}

func clusterResourceInfo(cluster *v1.Cluster) *v1.ClusterResources {
	if cluster == nil || cluster.Status == nil {
		return nil
	}

	return cluster.Status.ResourceInfo
}

func buildClusterResourcesFromResourceNodes(nodes []ResourceNode) *v1.ClusterResources {
	result := &v1.ClusterResources{
		ResourceStatus: v1.ResourceStatus{
			Allocatable: &v1.ResourceInfo{
				CPU:               0,
				Memory:            0,
				AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
			},
			Available: &v1.ResourceInfo{
				CPU:               0,
				Memory:            0,
				AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
			},
		},
		AcceleratorMetadata: make(map[v1.AcceleratorType]*v1.AcceleratorMetadata),
		NodeResources:       make(map[string]*v1.NodeResourceStatus),
	}

	for _, node := range nodes {
		if node.Status == nil {
			continue
		}

		if node.Status.Allocatable != nil {
			result.Allocatable.CPU += node.Status.Allocatable.CPU
			result.Allocatable.Memory += node.Status.Allocatable.Memory
			mergeAcceleratorGroups(result.Allocatable.AcceleratorGroups, node.Status.Allocatable.AcceleratorGroups)
			mergeAcceleratorMetadata(result.AcceleratorMetadata, node.Status.Allocatable.AcceleratorMetadata)
		}

		if node.Status.Available != nil {
			result.Available.CPU += node.Status.Available.CPU
			result.Available.Memory += node.Status.Available.Memory
			mergeAcceleratorGroups(result.Available.AcceleratorGroups, node.Status.Available.AcceleratorGroups)
			mergeAcceleratorMetadata(result.AcceleratorMetadata, node.Status.Available.AcceleratorMetadata)
		}

		mergeAcceleratorMetadata(result.AcceleratorMetadata, node.AcceleratorMetadata)
		result.NodeResources[node.ID] = node.Status
	}

	return result
}

func buildEndpointResourcesFromEndpointInstances(
	instances []EndpointInstanceResource,
	clusterResources *v1.ClusterResources,
) *v1.EndpointResourceStatus {
	if len(instances) == 0 {
		return nil
	}

	result := &v1.EndpointResourceStatus{
		Replicas: make([]v1.ReplicaDeviceAllocation, 0, len(instances)),
		Summary: &v1.EndpointResourceSummary{
			Products: make(map[v1.AcceleratorProduct]*v1.ProductUsage),
		},
	}

	for _, instance := range instances {
		replica := v1.ReplicaDeviceAllocation{
			InstanceID: instance.InstanceID,
			ReplicaID:  instance.ReplicaID,
			NodeID:     instance.NodeID,
			Devices:    instance.Devices,
		}
		result.Replicas = append(result.Replicas, replica)

		for _, device := range instance.Devices {
			product := v1.AcceleratorProduct(device.Product)
			if product == "" {
				continue
			}

			usage := result.Summary.Products[product]
			if usage == nil {
				usage = &v1.ProductUsage{}
				result.Summary.Products[product] = usage
			}

			usage.MemoryMiB += device.MemoryMiB
			usage.CoreUnits += device.CoreUnits
		}
	}

	applyClusterDeviceOrdersToEndpointResources(result, clusterResources)
	if len(result.Summary.Products) == 0 {
		result.Summary = nil
	}

	return result
}

func applyClusterDeviceOrdersToEndpointResources(
	resources *v1.EndpointResourceStatus,
	clusterResources *v1.ClusterResources,
) {
	if resources == nil || clusterResources == nil || len(clusterResources.NodeResources) == 0 {
		return
	}

	for i := range resources.Replicas {
		nodeResources := clusterResources.NodeResources[resources.Replicas[i].NodeID]
		if nodeResources == nil || len(nodeResources.Devices) == 0 {
			continue
		}

		orders := clusterDeviceOrdersByUUID(nodeResources.Devices)
		for j := range resources.Replicas[i].Devices {
			order := orders[resources.Replicas[i].Devices[j].UUID]
			if order == nil {
				continue
			}

			value := *order
			resources.Replicas[i].Devices[j].Order = &value
		}
	}
}

func clusterDeviceOrdersByUUID(devices []*v1.DeviceResource) map[string]*int {
	result := make(map[string]*int, len(devices))
	for _, device := range devices {
		if device == nil || device.UUID == "" || device.Order == nil {
			continue
		}

		order := *device.Order
		result[device.UUID] = &order
	}

	return result
}
