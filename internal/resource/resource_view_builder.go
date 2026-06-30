package resource

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type ResourceViewBuilder interface {
	BuildClusterResources(ctx context.Context, cluster *v1.Cluster) (*v1.ClusterResources, error)
	BuildEndpointResources(ctx context.Context, opts ListEndpointInstancesOptions) (*v1.EndpointResourceStatus, error)
}

type resourceViewBuilder struct {
	resourceClient ResourceClient
}

func NewResourceViewBuilder(resourceClient ResourceClient) ResourceViewBuilder {
	return &resourceViewBuilder{
		resourceClient: resourceClient,
	}
}

func ListNodesOptionsFromCluster(cluster *v1.Cluster) ListNodesOptions {
	return ListNodesOptions{
		AcceleratorVirtualizationEnabled: cluster != nil &&
			cluster.Spec != nil &&
			cluster.Spec.AcceleratorVirtualizationEnabled(),
	}
}

func (b *resourceViewBuilder) BuildClusterResources(
	ctx context.Context,
	cluster *v1.Cluster,
) (*v1.ClusterResources, error) {
	if b.resourceClient == nil {
		return nil, fmt.Errorf("resource client is nil")
	}

	nodes, err := b.resourceClient.ListNodes(ctx, ListNodesOptionsFromCluster(cluster))
	if err != nil {
		return nil, err
	}

	return buildClusterResourcesFromResourceNodes(nodes), nil
}

func (b *resourceViewBuilder) BuildEndpointResources(
	ctx context.Context,
	opts ListEndpointInstancesOptions,
) (*v1.EndpointResourceStatus, error) {
	if b.resourceClient == nil {
		return nil, fmt.Errorf("resource client is nil")
	}

	instances, err := b.resourceClient.ListEndpointInstances(ctx, opts)
	if err != nil {
		return nil, err
	}

	return buildEndpointResourcesFromEndpointInstances(instances), nil
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

func buildEndpointResourcesFromEndpointInstances(instances []EndpointInstanceResource) *v1.EndpointResourceStatus {
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

	if len(result.Summary.Products) == 0 {
		result.Summary = nil
	}

	return result
}
