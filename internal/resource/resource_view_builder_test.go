package resource

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
)

type fakeResourceClient struct {
	nodes             []ResourceNode
	endpointInstances []EndpointInstanceResource
	err               error
	endpointErr       error
	called            bool
	endpointCalled    bool
	cluster           *v1.Cluster
	endpoint          *v1.Endpoint
}

func (c *fakeResourceClient) ListNodes(_ context.Context, cluster *v1.Cluster) ([]ResourceNode, error) {
	c.called = true
	c.cluster = cluster
	if c.err != nil {
		return nil, c.err
	}

	return c.nodes, nil
}

func (c *fakeResourceClient) ListEndpointInstances(
	_ context.Context,
	cluster *v1.Cluster,
	endpoint *v1.Endpoint,
) ([]EndpointInstanceResource, error) {
	c.endpointCalled = true
	c.cluster = cluster
	c.endpoint = endpoint
	if c.endpointErr != nil {
		return nil, c.endpointErr
	}

	return c.endpointInstances, nil
}

func TestResourceViewBuilderBuildClusterResources(t *testing.T) {
	node1Status := &v1.NodeResourceStatus{
		ResourceStatus: v1.ResourceStatus{
			Allocatable: &v1.ResourceInfo{
				CPU:    8,
				Memory: 32,
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeNVIDIAGPU: {
						Quantity: 2,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"NVIDIA_A100": 2,
						},
					},
				},
			},
			Available: &v1.ResourceInfo{
				CPU:    6,
				Memory: 24,
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeNVIDIAGPU: {
						Quantity: 1,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"NVIDIA_A100": 1,
						},
					},
				},
			},
		},
		Devices: []*v1.DeviceResource{
			{
				UUID:    "GPU-1",
				Product: "NVIDIA_A100",
				Health:  true,
			},
		},
	}
	node2Status := &v1.NodeResourceStatus{
		ResourceStatus: v1.ResourceStatus{
			Allocatable: &v1.ResourceInfo{
				CPU:    4,
				Memory: 16,
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeNVIDIAGPU: {
						Quantity: 1,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"NVIDIA_T4": 1,
						},
					},
				},
			},
			Available: &v1.ResourceInfo{
				CPU:    2,
				Memory: 8,
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeNVIDIAGPU: {
						Quantity: 1,
						ProductGroups: map[v1.AcceleratorProduct]float64{
							"NVIDIA_T4": 1,
						},
					},
				},
			},
		},
	}

	client := &fakeResourceClient{
		nodes: []ResourceNode{
			{
				ID:     "node-1",
				Status: node1Status,
				AcceleratorMetadata: map[v1.AcceleratorType]*v1.AcceleratorMetadata{
					v1.AcceleratorTypeNVIDIAGPU: {
						Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata{
							"NVIDIA_A100": {
								MemoryTotalMiB: 81920,
							},
						},
					},
				},
			},
			{
				ID:     "node-2",
				Status: node2Status,
				AcceleratorMetadata: map[v1.AcceleratorType]*v1.AcceleratorMetadata{
					v1.AcceleratorTypeNVIDIAGPU: {
						Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata{
							"NVIDIA_T4": {
								MemoryTotalMiB: 15360,
							},
						},
					},
				},
			},
		},
	}

	resources, err := NewResourceViewBuilder(client).BuildClusterResources(context.Background(), &v1.Cluster{
		Spec: &v1.ClusterSpec{
			AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
				Enabled: true,
			},
		},
	})

	require.NoError(t, err)
	require.True(t, client.called)
	require.Equal(t, float64(12), resources.Allocatable.CPU)
	require.Equal(t, float64(48), resources.Allocatable.Memory)
	require.Equal(t, float64(3), resources.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].Quantity)
	require.Equal(t, float64(2), resources.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].ProductGroups["NVIDIA_A100"])
	require.Equal(t, float64(1), resources.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].ProductGroups["NVIDIA_T4"])
	require.Equal(t, float64(8), resources.Available.CPU)
	require.Equal(t, float64(32), resources.Available.Memory)
	require.Equal(t, float64(2), resources.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].Quantity)
	require.Equal(t, float64(81920), resources.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_A100"].MemoryTotalMiB)
	require.Equal(t, float64(15360), resources.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_T4"].MemoryTotalMiB)
	require.Same(t, node1Status, resources.NodeResources["node-1"])
	require.Same(t, node2Status, resources.NodeResources["node-2"])
	require.Len(t, resources.NodeResources["node-1"].Devices, 1)
}

func TestResourceViewBuilderReturnsClientError(t *testing.T) {
	expectedErr := errors.New("list nodes")
	client := &fakeResourceClient{err: expectedErr}

	resources, err := NewResourceViewBuilder(client).BuildClusterResources(context.Background(), &v1.Cluster{})

	require.ErrorIs(t, err, expectedErr)
	require.Nil(t, resources)
	require.True(t, client.called)
}

func TestResourceViewBuilderBuildEndpointResources(t *testing.T) {
	order := 1
	client := &fakeResourceClient{
		endpointInstances: []EndpointInstanceResource{
			{
				InstanceID: "endpoint-abc",
				ReplicaID:  "uid-1",
				NodeID:     "node-1",
				Devices: []v1.DeviceAllocation{
					{
						UUID:      "GPU-1",
						Product:   "Tesla-T4",
						MemoryMiB: 15360,
						CoreUnits: 100,
						NodeID:    "node-1",
					},
				},
			},
			{
				InstanceID: "endpoint-def",
				ReplicaID:  "uid-2",
				NodeID:     "node-2",
				Devices: []v1.DeviceAllocation{
					{
						UUID:      "GPU-2",
						Product:   "Tesla-T4",
						MemoryMiB: 7680,
						CoreUnits: 50,
						NodeID:    "node-2",
					},
				},
			},
		},
	}

	endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Name: "endpoint"}}
	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Name: "cluster", Workspace: "default"},
		Status: &v1.ClusterStatus{
			ResourceInfo: &v1.ClusterResources{
				NodeResources: map[string]*v1.NodeResourceStatus{
					"node-1": {
						Devices: []*v1.DeviceResource{
							{UUID: "GPU-1", Order: &order},
						},
					},
				},
			},
		},
	}
	resources, err := NewResourceViewBuilder(client).BuildEndpointResources(context.Background(), cluster, endpoint)

	require.NoError(t, err)
	require.False(t, client.called)
	require.True(t, client.endpointCalled)
	require.Same(t, cluster, client.cluster)
	require.Same(t, endpoint, client.endpoint)
	require.Len(t, resources.Replicas, 2)
	require.Equal(t, "endpoint-abc", resources.Replicas[0].InstanceID)
	require.NotNil(t, resources.Replicas[0].Devices[0].Order)
	require.Equal(t, 1, *resources.Replicas[0].Devices[0].Order)
	require.Equal(t, int64(23040), resources.Summary.Products["Tesla-T4"].MemoryMiB)
	require.Equal(t, int64(150), resources.Summary.Products["Tesla-T4"].CoreUnits)
}

func TestResourceViewBuilderBuildEndpointResourcesReturnsNilForEmptyInstances(t *testing.T) {
	client := &fakeResourceClient{}

	endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Name: "endpoint"}}
	cluster := &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster", Workspace: "default"}}
	resources, err := NewResourceViewBuilder(client).BuildEndpointResources(context.Background(), cluster, endpoint)

	require.NoError(t, err)
	require.True(t, client.endpointCalled)
	require.Same(t, cluster, client.cluster)
	require.Same(t, endpoint, client.endpoint)
	require.Nil(t, resources)
}
