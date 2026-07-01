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
	listNodesOptions  ListNodesOptions
}

type fakeStaticNodeLister struct {
	nodes []v1.StaticNode
	err   error
}

func (l *fakeStaticNodeLister) ListByCluster(
	_ context.Context,
	_ string,
	_ string,
) ([]v1.StaticNode, error) {
	return l.nodes, l.err
}

func (c *fakeResourceClient) ListNodes(_ context.Context, opts ListNodesOptions) ([]ResourceNode, error) {
	c.called = true
	c.listNodesOptions = opts
	if c.err != nil {
		return nil, c.err
	}

	return c.nodes, nil
}

func (c *fakeResourceClient) ListEndpointInstances(
	_ context.Context,
	_ ListEndpointInstancesOptions,
) ([]EndpointInstanceResource, error) {
	c.endpointCalled = true
	if c.endpointErr != nil {
		return nil, c.endpointErr
	}

	return c.endpointInstances, nil
}

func TestStaticNodeClusterResourceClientListNodesEnrichesStaticDevices(t *testing.T) {
	rayClient := &fakeResourceClient{
		nodes: []ResourceNode{
			{
				ID: "10.0.0.10",
				Status: &v1.NodeResourceStatus{
					ResourceStatus: v1.ResourceStatus{
						Allocatable: &v1.ResourceInfo{
							AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
								v1.AcceleratorTypeNVIDIAGPU: {
									Quantity: 2,
									ProductGroups: map[v1.AcceleratorProduct]float64{
										"NVIDIA_Tesla_T4": 2,
									},
								},
							},
						},
						Available: &v1.ResourceInfo{
							AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
								v1.AcceleratorTypeNVIDIAGPU: {
									Quantity: 1,
									ProductGroups: map[v1.AcceleratorProduct]float64{
										"NVIDIA_Tesla_T4": 1,
									},
								},
							},
						},
					},
				},
			},
		},
	}
	staticNodes := &fakeStaticNodeLister{
		nodes: []v1.StaticNode{
			{
				Spec: &v1.StaticNodeSpec{IP: "10.0.0.10"},
				Status: &v1.StaticNodeStatus{
					Accelerator: &v1.StaticNodeAcceleratorStatus{
						Type:         string(v1.AcceleratorTypeNVIDIAGPU),
						ProductModel: "Tesla T4",
						Devices: []v1.StaticNodeAcceleratorDeviceStatus{
							{
								UUID:         "GPU-1",
								ProductModel: "Tesla T4",
								MemoryMiB:    15360,
								Healthy:      true,
							},
						},
					},
				},
			},
		},
	}
	client := NewStaticNodeClusterResourceClient(
		rayClient,
		staticNodes,
		"default",
		"static-a",
	)

	nodes, err := client.ListNodes(context.Background(), ListNodesOptions{})

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.NotNil(t, nodes[0].Status)
	require.Len(t, nodes[0].Status.Devices, 1)
	require.Equal(t, "GPU-1", nodes[0].Status.Devices[0].UUID)
	require.Equal(t, "NVIDIA_Tesla_T4", nodes[0].Status.Devices[0].Product)
	require.Contains(t, nodes[0].AcceleratorMetadata, v1.AcceleratorTypeNVIDIAGPU)
	require.Equal(t, float64(15360),
		nodes[0].AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_Tesla_T4"].MemoryTotalMiB)
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
	require.True(t, client.listNodesOptions.AcceleratorVirtualizationEnabled)
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

	resources, err := NewResourceViewBuilder(client).BuildEndpointResources(
		context.Background(),
		ListEndpointInstancesOptions{EndpointName: "endpoint"},
	)

	require.NoError(t, err)
	require.True(t, client.endpointCalled)
	require.Len(t, resources.Replicas, 2)
	require.Equal(t, "endpoint-abc", resources.Replicas[0].InstanceID)
	require.Equal(t, int64(23040), resources.Summary.Products["Tesla-T4"].MemoryMiB)
	require.Equal(t, int64(150), resources.Summary.Products["Tesla-T4"].CoreUnits)
}

func TestResourceViewBuilderBuildEndpointResourcesReturnsNilForEmptyInstances(t *testing.T) {
	client := &fakeResourceClient{}

	resources, err := NewResourceViewBuilder(client).BuildEndpointResources(
		context.Background(),
		ListEndpointInstancesOptions{EndpointName: "endpoint"},
	)

	require.NoError(t, err)
	require.True(t, client.endpointCalled)
	require.Nil(t, resources)
}
