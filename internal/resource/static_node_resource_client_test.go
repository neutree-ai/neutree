package resource

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type staticNodeBaseClient struct {
	nodes []ResourceNode
	err   error
}

func (c staticNodeBaseClient) ListNodes(context.Context, *v1.Cluster) ([]ResourceNode, error) {
	if c.err != nil {
		return nil, c.err
	}

	return c.nodes, nil
}

func (c staticNodeBaseClient) ListEndpointInstances(context.Context, *v1.Cluster, *v1.Endpoint) ([]EndpointInstanceResource, error) {
	return nil, nil
}

type fakeStaticNodeLister struct {
	nodes  []v1.StaticNode
	err    error
	option storage.ListOption
}

func (l *fakeStaticNodeLister) ListStaticNode(option storage.ListOption) ([]v1.StaticNode, error) {
	l.option = option
	if l.err != nil {
		return nil, l.err
	}

	return l.nodes, nil
}

func newStaticNodeResourceClientForTest(
	nodes []*v1.StaticNode,
	baseClient ResourceClient,
) *StaticNodeResourceClient {
	return newStaticNodeResourceClientForTestWithDashboard(nodes, baseClient, nil, map[string]struct{}{"actor-1": {}})
}

func newStaticNodeResourceClientForTestWithDashboard(
	nodes []*v1.StaticNode,
	baseClient ResourceClient,
	dashboardClient dashboard.DashboardService,
	runningActors map[string]struct{},
) *StaticNodeResourceClient {
	_, lister := staticNodeClusterAndListerForTest(nodes)

	return NewStaticNodeClusterResourceClientWithDashboard(lister, baseClient, dashboardClient, runningActors)
}

func staticNodeClusterAndListerForTest(nodes []*v1.StaticNode) (*v1.Cluster, *fakeStaticNodeLister) {
	const (
		workspace = "default"
		cluster   = "static-cluster"
	)

	items := make([]v1.StaticNode, 0, len(nodes))
	for _, node := range nodes {
		if node == nil {
			continue
		}

		copied := *node
		if copied.Metadata == nil {
			copied.Metadata = &v1.Metadata{}
		} else {
			metadata := *copied.Metadata
			copied.Metadata = &metadata
		}
		if copied.Spec == nil {
			copied.Spec = &v1.StaticNodeSpec{}
		} else {
			spec := *copied.Spec
			copied.Spec = &spec
		}
		if copied.Metadata.Workspace == "" {
			copied.Metadata.Workspace = workspace
		}
		copied.Spec.Cluster = cluster

		items = append(items, copied)
	}

	return &v1.Cluster{
		Metadata: &v1.Metadata{Name: cluster, Workspace: workspace},
	}, &fakeStaticNodeLister{nodes: items}
}

func staticNodeClusterForTest() *v1.Cluster {
	cluster, _ := staticNodeClusterAndListerForTest(nil)
	return cluster
}

func staticNodeBaseResourceNodeForTest(
	id string,
	cpu float64,
	memory float64,
	availableCPU float64,
	availableMemory float64,
	product v1.AcceleratorProduct,
) ResourceNode {
	node := ResourceNode{
		ID: id,
		Status: &v1.NodeResourceStatus{
			ResourceStatus: v1.ResourceStatus{
				Allocatable: &v1.ResourceInfo{CPU: cpu, Memory: memory},
				Available:   &v1.ResourceInfo{CPU: availableCPU, Memory: availableMemory},
			},
		},
	}
	if product == "" {
		return node
	}

	node.Status.Allocatable.AcceleratorGroups = map[v1.AcceleratorType]*v1.AcceleratorGroup{
		v1.AcceleratorTypeNVIDIAGPU: {
			Quantity: 1,
			ProductGroups: map[v1.AcceleratorProduct]float64{
				product: 1,
			},
		},
	}
	node.Status.Available.AcceleratorGroups = map[v1.AcceleratorType]*v1.AcceleratorGroup{
		v1.AcceleratorTypeNVIDIAGPU: {
			Quantity: 1,
			ProductGroups: map[v1.AcceleratorProduct]float64{
				product: 1,
			},
		},
	}

	return node
}

func testEndpoint(name string, product string) *v1.Endpoint {
	return &v1.Endpoint{
		Metadata: &v1.Metadata{Name: name, Workspace: "default"},
		Spec: &v1.EndpointSpec{
			Resources: &v1.ResourceSpec{
				Accelerator: map[string]string{
					v1.AcceleratorProductKey: product,
				},
			},
		},
	}
}

func TestStaticNodeResourceClientBuildsResourceNodesFromDeviceSnapshots(t *testing.T) {
	client := newStaticNodeResourceClientForTest([]*v1.StaticNode{
		{
			Metadata: &v1.Metadata{Name: "head-0", Workspace: "default"},
			Spec: &v1.StaticNodeSpec{
				IP: "192.168.19.218",
			},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
					Devices: []v1.StaticNodeAcceleratorDeviceStatus{
						{UUID: "GPU-abc", ProductModel: "NVIDIA_Tesla_T4", MinorNumber: intPtr(3), MemoryMiB: 15360, Healthy: true},
						{UUID: "GPU-def", ProductModel: "NVIDIA_Tesla_T4", MinorNumber: intPtr(0), MemoryMiB: 15360, Healthy: false},
					},
				},
				Allocations: []v1.StaticNodeAllocationStatus{
					{
						Devices: []v1.DeviceAllocation{
							{UUID: "GPU-abc", MemoryMiB: 7680, CoreUnits: 50},
							{UUID: "GPU-def", MemoryMiB: 7680, CoreUnits: 50},
						},
					},
				},
			},
		},
	}, staticNodeBaseClient{nodes: []ResourceNode{
		staticNodeBaseResourceNodeForTest("192.168.19.218", 0, 0, 0, 0, "NVIDIA_Tesla_T4"),
	}})

	nodes, err := client.ListNodes(context.Background(), staticNodeClusterForTest())

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "192.168.19.218", nodes[0].ID)
	require.NotNil(t, nodes[0].Status)
	assert.Equal(t, float64(1), nodes[0].Status.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].Quantity)
	allocatableProduct := nodes[0].Status.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].
		Products["NVIDIA_Tesla_T4"]
	require.NotNil(t, allocatableProduct.Virtualization)
	assert.Equal(t, float64(1), allocatableProduct.Quantity)
	assert.Equal(t, float64(30720), allocatableProduct.Virtualization.MemoryMiB)
	assert.Equal(t, float64(200), allocatableProduct.Virtualization.CoreUnits)
	assert.Equal(t, float64(1), nodes[0].Status.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].Quantity)
	availableProduct := nodes[0].Status.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].
		Products["NVIDIA_Tesla_T4"]
	require.NotNil(t, availableProduct.Virtualization)
	assert.Equal(t, float64(1), availableProduct.Quantity)
	assert.Equal(t, float64(15360), availableProduct.Virtualization.MemoryMiB)
	assert.Equal(t, float64(100), availableProduct.Virtualization.CoreUnits)
	require.Len(t, nodes[0].Status.Devices, 2)
	assert.True(t, nodes[0].Status.Devices[0].Health)
	assert.Equal(t, int64(7680), nodes[0].Status.Devices[0].Available.MemoryMiB)
	assert.Equal(t, int64(50), nodes[0].Status.Devices[0].Available.CoreUnits)
	assert.False(t, nodes[0].Status.Devices[1].Health)
	assert.Equal(t, &v1.DeviceResourcePool{MemoryMiB: 15360, CoreUnits: 100}, nodes[0].Status.Devices[1].Allocatable)
	assert.Equal(t, nodes[0].Status.Devices[0].Available, nodes[0].Status.Devices[1].Available)
	require.NotNil(t, nodes[0].Status.Devices[0].Order)
	assert.Equal(t, 1, *nodes[0].Status.Devices[0].Order)
	require.NotNil(t, nodes[0].Status.Devices[1].Order)
	assert.Equal(t, 0, *nodes[0].Status.Devices[1].Order)
	require.NotNil(t, nodes[0].AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU])
	assert.Equal(t, float64(15360),
		nodes[0].AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_Tesla_T4"].MemoryTotalMiB)
}

func TestStaticNodeResourceClientCompletesCPUAndMemoryFromBaseResources(t *testing.T) {
	client := newStaticNodeResourceClientForTest([]*v1.StaticNode{
		{
			Metadata: &v1.Metadata{Name: "head-0", Workspace: "default"},
			Spec: &v1.StaticNodeSpec{
				IP: "192.168.19.218",
			},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
					Devices: []v1.StaticNodeAcceleratorDeviceStatus{
						{UUID: "GPU-abc", ProductModel: "NVIDIA_Tesla_T4", MemoryMiB: 15360, Healthy: true},
					},
				},
			},
		},
	}, staticNodeBaseClient{nodes: []ResourceNode{
		staticNodeBaseResourceNodeForTest("192.168.19.218", 32, 64, 24, 48, "NVIDIA_Tesla_T4"),
	}})

	nodes, err := client.ListNodes(context.Background(), staticNodeClusterForTest())

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.NotNil(t, nodes[0].Status)
	require.NotNil(t, nodes[0].Status.Allocatable)
	require.NotNil(t, nodes[0].Status.Available)
	assert.Equal(t, float64(32), nodes[0].Status.Allocatable.CPU)
	assert.Equal(t, float64(64), nodes[0].Status.Allocatable.Memory)
	assert.Equal(t, float64(24), nodes[0].Status.Available.CPU)
	assert.Equal(t, float64(48), nodes[0].Status.Available.Memory)

	resources := buildClusterResourcesFromResourceNodes(nodes)
	assert.Equal(t, float64(32), resources.Allocatable.CPU)
	assert.Equal(t, float64(64), resources.Allocatable.Memory)
	assert.Equal(t, float64(24), resources.Available.CPU)
	assert.Equal(t, float64(48), resources.Available.Memory)
}

func TestStaticNodeResourceClientUsesBaseRayAcceleratorQuantities(t *testing.T) {
	client := newStaticNodeResourceClientForTest([]*v1.StaticNode{
		{
			Metadata: &v1.Metadata{Name: "head-0", Workspace: "default"},
			Spec: &v1.StaticNodeSpec{
				IP: "192.168.19.218",
			},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
					Devices: []v1.StaticNodeAcceleratorDeviceStatus{
						{UUID: "GPU-abc", ProductModel: "NVIDIA_Tesla_T4", MemoryMiB: 15360, Healthy: false},
						{UUID: "GPU-def", ProductModel: "NVIDIA_Tesla_T4", MemoryMiB: 15360, Healthy: false},
					},
				},
				Allocations: []v1.StaticNodeAllocationStatus{
					{
						Devices: []v1.DeviceAllocation{
							{UUID: "GPU-abc", MemoryMiB: 15360, CoreUnits: 100},
							{UUID: "GPU-def", MemoryMiB: 7680, CoreUnits: 50},
						},
					},
				},
			},
		},
	}, staticNodeBaseClient{nodes: []ResourceNode{
		{
			ID: "192.168.19.218",
			Status: &v1.NodeResourceStatus{
				ResourceStatus: v1.ResourceStatus{
					Allocatable: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Quantity: 1.5,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"NVIDIA_Tesla_T4": 1.5,
								},
							},
						},
					},
					Available: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Quantity: 0.5,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"NVIDIA_Tesla_T4": 0.5,
								},
							},
						},
					},
				},
			},
		},
	}})

	nodes, err := client.ListNodes(context.Background(), staticNodeClusterForTest())

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	allocatable := nodes[0].Status.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.NotNil(t, allocatable)
	assert.Equal(t, float64(1.5), allocatable.Quantity)
	assert.Equal(t, float64(1.5), allocatable.ProductGroups["NVIDIA_Tesla_T4"])
	assert.Equal(t, float64(1.5), allocatable.Products["NVIDIA_Tesla_T4"].Quantity)
	require.NotNil(t, allocatable.Products["NVIDIA_Tesla_T4"].Virtualization)
	assert.Equal(t, float64(30720), allocatable.Products["NVIDIA_Tesla_T4"].Virtualization.MemoryMiB)
	assert.Equal(t, float64(200), allocatable.Products["NVIDIA_Tesla_T4"].Virtualization.CoreUnits)

	available := nodes[0].Status.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.NotNil(t, available)
	assert.Equal(t, float64(0.5), available.Quantity)
	assert.Equal(t, float64(0.5), available.ProductGroups["NVIDIA_Tesla_T4"])
	assert.Equal(t, float64(0.5), available.Products["NVIDIA_Tesla_T4"].Quantity)
	require.NotNil(t, available.Products["NVIDIA_Tesla_T4"].Virtualization)
	assert.Equal(t, float64(7680), available.Products["NVIDIA_Tesla_T4"].Virtualization.MemoryMiB)
	assert.Equal(t, float64(50), available.Products["NVIDIA_Tesla_T4"].Virtualization.CoreUnits)
}

func TestStaticNodeResourceClientUsesBaseRayAvailableQuantityWhenSnapshotHasNoAvailableDevices(t *testing.T) {
	client := newStaticNodeResourceClientForTest([]*v1.StaticNode{
		{
			Metadata: &v1.Metadata{Name: "head-0", Workspace: "default"},
			Spec: &v1.StaticNodeSpec{
				IP: "192.168.19.218",
			},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
					Devices: []v1.StaticNodeAcceleratorDeviceStatus{
						{UUID: "GPU-abc", ProductModel: "NVIDIA_Tesla_T4", MemoryMiB: 15360, Healthy: false},
						{UUID: "GPU-def", ProductModel: "NVIDIA_Tesla_T4", MemoryMiB: 15360, Healthy: false},
					},
				},
				Allocations: []v1.StaticNodeAllocationStatus{
					{
						Devices: []v1.DeviceAllocation{
							{UUID: "GPU-abc", MemoryMiB: 15360, CoreUnits: 100},
							{UUID: "GPU-def", MemoryMiB: 15360, CoreUnits: 100},
						},
					},
				},
			},
		},
	}, staticNodeBaseClient{nodes: []ResourceNode{
		{
			ID: "192.168.19.218",
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
								Quantity: 0.5,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"NVIDIA_Tesla_T4": 0.5,
								},
							},
						},
					},
				},
			},
		},
	}})

	nodes, err := client.ListNodes(context.Background(), staticNodeClusterForTest())

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	available := nodes[0].Status.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.NotNil(t, available)
	assert.Equal(t, float64(0.5), available.Quantity)
	assert.Equal(t, float64(0.5), available.ProductGroups["NVIDIA_Tesla_T4"])
	assert.Equal(t, float64(0.5), available.Products["NVIDIA_Tesla_T4"].Quantity)
	assert.Nil(t, available.Products["NVIDIA_Tesla_T4"].Virtualization)
}

func TestStaticNodeResourceClientUsesRayProductKeyFromBaseResources(t *testing.T) {
	node := &v1.StaticNode{
		Metadata: &v1.Metadata{Name: "head-0", Workspace: "default"},
		Spec: &v1.StaticNodeSpec{
			IP: "192.168.19.218",
		},
		Status: &v1.StaticNodeStatus{
			Accelerator: &v1.StaticNodeAcceleratorStatus{
				Type: v1.AcceleratorTypeNVIDIAGPU.String(),
				Devices: []v1.StaticNodeAcceleratorDeviceStatus{
					{
						UUID:         "GPU-abc",
						ProductName:  "NVIDIA L20",
						ProductModel: "NVIDIA L20",
						MemoryMiB:    49152,
						Healthy:      true,
					},
				},
			},
		},
	}
	client := newStaticNodeResourceClientForTest([]*v1.StaticNode{
		node,
	}, staticNodeBaseClient{nodes: []ResourceNode{
		{
			ID: "192.168.19.218",
			Status: &v1.NodeResourceStatus{
				ResourceStatus: v1.ResourceStatus{
					Allocatable: &v1.ResourceInfo{
						CPU:    32,
						Memory: 64,
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Quantity: 1,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"NVIDIA-L20": 1,
								},
							},
						},
					},
					Available: &v1.ResourceInfo{
						CPU:    32,
						Memory: 64,
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Quantity: 1,
								ProductGroups: map[v1.AcceleratorProduct]float64{
									"NVIDIA-L20": 1,
								},
							},
						},
					},
				},
			},
		},
	}})

	nodes, err := client.ListNodes(context.Background(), staticNodeClusterForTest())

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Len(t, nodes[0].Status.Devices, 1)
	assert.Equal(t, "NVIDIA-L20", nodes[0].Status.Devices[0].Product)
	assert.Equal(t, "NVIDIA L20", node.Status.Accelerator.Devices[0].ProductModel)
	assert.Contains(t,
		nodes[0].Status.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].ProductGroups,
		v1.AcceleratorProduct("NVIDIA-L20"))
	require.NotNil(t, nodes[0].AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU])
	assert.Contains(t,
		nodes[0].AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products,
		v1.AcceleratorProduct("NVIDIA-L20"))
}

func TestStaticNodeResourceClientListEndpointInstancesUsesEndpointProductKey(t *testing.T) {
	client := newStaticNodeResourceClientForTest([]*v1.StaticNode{
		{
			Metadata: &v1.Metadata{Name: "head-0", Workspace: "default"},
			Spec:     &v1.StaticNodeSpec{IP: "192.168.19.218"},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
				},
				Allocations: []v1.StaticNodeAllocationStatus{
					{
						Workspace: "default",
						Endpoint:  "chat",
						ReplicaID: "replica-a",
						Devices: []v1.DeviceAllocation{
							{UUID: "GPU-abc", Product: "NVIDIA-L20", MemoryMiB: 49152, CoreUnits: 100},
						},
					},
				},
			},
		},
		{
			Metadata: &v1.Metadata{Name: "worker-0", Workspace: "default"},
			Spec:     &v1.StaticNodeSpec{IP: "192.168.19.219"},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
				},
				Allocations: []v1.StaticNodeAllocationStatus{
					{
						Workspace: "default",
						Endpoint:  "embed",
						Devices: []v1.DeviceAllocation{
							{UUID: "GPU-def", Product: "NVIDIA-L20", MemoryMiB: 49152, CoreUnits: 100},
						},
					},
				},
			},
		},
	}, nil)

	instances, err := client.ListEndpointInstances(context.Background(), staticNodeClusterForTest(), testEndpoint("chat", "NVIDIA-L20"))

	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "replica-a", instances[0].InstanceID)
	assert.Equal(t, "replica-a", instances[0].ReplicaID)
	assert.Equal(t, "192.168.19.218", instances[0].NodeID)
	require.Len(t, instances[0].Devices, 1)
	assert.Equal(t, "GPU-abc", instances[0].Devices[0].UUID)
	assert.Equal(t, "NVIDIA-L20", instances[0].Devices[0].Product)
	assert.Equal(t, "192.168.19.218", instances[0].Devices[0].NodeID)
}

func TestStaticNodeResourceClientReadsVLLMStartupMemoryFromRayLogs(t *testing.T) {
	dashboard := &dashboardmocks.MockDashboardService{}
	dashboard.On("GetActorLogHead", mock.Anything, "actor-1", "out", startupLogLimitBytes).
		Return("Model loading took 10.5 GiB\n", nil)
	dashboard.On("GetActorLogHead", mock.Anything, "actor-1", "err", startupLogLimitBytes).
		Return("Available KV cache memory: 4.25 GiB\n", nil)

	client := newStaticNodeResourceClientForTestWithDashboard([]*v1.StaticNode{
		{
			Metadata: &v1.Metadata{Name: "head-0", Workspace: "default"},
			Spec:     &v1.StaticNodeSpec{IP: "192.168.19.218"},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.AcceleratorTypeNVIDIAGPU.String()},
				Allocations: []v1.StaticNodeAllocationStatus{{
					Workspace: "default",
					Endpoint:  "chat",
					ReplicaID: "replica-a",
					RuntimeID: "actor-1",
					Devices:   []v1.DeviceAllocation{{UUID: "GPU-abc", Product: "NVIDIA-L20", MemoryMiB: 49152, CoreUnits: 100}},
				}},
			},
		},
	}, nil, dashboard, map[string]struct{}{"actor-1": {}})

	endpoint := testEndpoint("chat", "NVIDIA-L20")
	endpoint.Spec.Engine = &v1.EndpointEngineSpec{Engine: "vllm"}
	instances, err := client.ListEndpointInstances(context.Background(), staticNodeClusterForTest(), endpoint)

	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.NotNil(t, instances[0].ModelWeightsMiB)
	require.InDelta(t, 10752.0, *instances[0].ModelWeightsMiB, 0.01)
	require.NotNil(t, instances[0].KVCacheCapacityMiB)
	require.InDelta(t, 4352.0, *instances[0].KVCacheCapacityMiB, 0.01)
	dashboard.AssertExpectations(t)
}

func startupMemoryStaticNode() *v1.StaticNode {
	return &v1.StaticNode{
		Metadata: &v1.Metadata{Name: "head-0", Workspace: "default"},
		Spec:     &v1.StaticNodeSpec{IP: "192.168.19.218"},
		Status: &v1.StaticNodeStatus{
			Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.AcceleratorTypeNVIDIAGPU.String()},
			Allocations: []v1.StaticNodeAllocationStatus{{
				Workspace: "default", Endpoint: "chat", ReplicaID: "replica-a", RuntimeID: "actor-1",
				Devices: []v1.DeviceAllocation{{UUID: "GPU-abc", Product: "NVIDIA-L20", MemoryMiB: 49152, CoreUnits: 100}},
			}},
		},
	}
}

func TestStaticNodeResourceClientReusesStartupMemoryForRunningActor(t *testing.T) {
	dashboard := &dashboardmocks.MockDashboardService{}
	client := newStaticNodeResourceClientForTestWithDashboard([]*v1.StaticNode{startupMemoryStaticNode()}, nil, dashboard, map[string]struct{}{"actor-1": {}})
	weights, kv := 1024.0, 256.0
	endpoint := testEndpoint("chat", "NVIDIA-L20")
	endpoint.Spec.Engine = &v1.EndpointEngineSpec{Engine: "vllm"}
	endpoint.Status = &v1.EndpointStatus{Resources: &v1.EndpointResourceStatus{Replicas: []v1.ReplicaDeviceAllocation{{
		ReplicaID: "replica-a", ContainerID: "actor-1", ModelWeightsMiB: &weights, KVCacheCapacityMiB: &kv,
	}}}}

	instances, err := client.ListEndpointInstances(context.Background(), staticNodeClusterForTest(), endpoint)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, &weights, instances[0].ModelWeightsMiB)
	assert.Equal(t, &kv, instances[0].KVCacheCapacityMiB)
	dashboard.AssertNotCalled(t, "GetActorLogHead", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestStaticNodeResourceClientDoesNotCacheIncompleteOrFailedStartupLogs(t *testing.T) {
	dashboard := &dashboardmocks.MockDashboardService{}
	dashboard.On("GetActorLogHead", mock.Anything, "actor-1", mock.Anything, startupLogLimitBytes).
		Return("", errors.New("dashboard unavailable"))
	client := newStaticNodeResourceClientForTestWithDashboard([]*v1.StaticNode{startupMemoryStaticNode()}, nil, dashboard, map[string]struct{}{"actor-1": {}})
	endpoint := testEndpoint("chat", "NVIDIA-L20")
	endpoint.Spec.Engine = &v1.EndpointEngineSpec{Engine: "vllm"}

	instances, err := client.ListEndpointInstances(context.Background(), staticNodeClusterForTest(), endpoint)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Empty(t, instances[0].ContainerID)
	assert.Nil(t, instances[0].ModelWeightsMiB)
	assert.Nil(t, instances[0].KVCacheCapacityMiB)
	dashboard.AssertExpectations(t)
}

func TestStaticNodeResourceClientSkipsNonRunningActorsAndClearsKVOverride(t *testing.T) {
	dashboard := &dashboardmocks.MockDashboardService{}
	client := newStaticNodeResourceClientForTestWithDashboard([]*v1.StaticNode{startupMemoryStaticNode()}, nil, dashboard, map[string]struct{}{})
	endpoint := testEndpoint("chat", "NVIDIA-L20")
	endpoint.Spec.Engine = &v1.EndpointEngineSpec{Engine: "vllm"}
	endpoint.Spec.Variables = map[string]interface{}{"engine_args": map[string]interface{}{"num-gpu-blocks-override": 12}}

	instances, err := client.ListEndpointInstances(context.Background(), staticNodeClusterForTest(), endpoint)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	dashboard.AssertNotCalled(t, "GetActorLogHead", mock.Anything, mock.Anything, mock.Anything, mock.Anything)

	dashboard = &dashboardmocks.MockDashboardService{}
	dashboard.On("GetActorLogHead", mock.Anything, "actor-1", "out", startupLogLimitBytes).Return("Model loading took 10 GiB", nil)
	dashboard.On("GetActorLogHead", mock.Anything, "actor-1", "err", startupLogLimitBytes).Return("Available KV cache memory: 4 GiB", nil)
	client = newStaticNodeResourceClientForTestWithDashboard([]*v1.StaticNode{startupMemoryStaticNode()}, nil, dashboard, map[string]struct{}{"actor-1": {}})
	instances, err = client.ListEndpointInstances(context.Background(), staticNodeClusterForTest(), endpoint)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.NotNil(t, instances[0].ModelWeightsMiB)
	assert.Nil(t, instances[0].KVCacheCapacityMiB)
	dashboard.AssertExpectations(t)
}

func TestStaticNodeResourceClientListEndpointInstancesReturnsNilWhenStaticNodesDoNotExist(t *testing.T) {
	client := newStaticNodeResourceClientForTest(nil, nil)

	instances, err := client.ListEndpointInstances(context.Background(), staticNodeClusterForTest(), testEndpoint("chat", "NVIDIA-L20"))

	require.NoError(t, err)
	assert.Nil(t, instances)
}

func TestStaticNodeResourceClientUsesBaseResourcesForCPUOnlySnapshots(t *testing.T) {
	client := newStaticNodeResourceClientForTest([]*v1.StaticNode{
		{
			Metadata: &v1.Metadata{Name: "head-0"},
			Spec:     &v1.StaticNodeSpec{IP: "192.168.19.218"},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU},
			},
		},
		{
			Metadata: &v1.Metadata{Name: "worker-0"},
			Spec:     &v1.StaticNodeSpec{IP: "192.168.19.219"},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
					Devices: []v1.StaticNodeAcceleratorDeviceStatus{
						{UUID: "GPU-abc", ProductModel: "NVIDIA_Tesla_T4", MemoryMiB: 15360, Healthy: true},
					},
				},
			},
		},
	}, staticNodeBaseClient{nodes: []ResourceNode{
		staticNodeBaseResourceNodeForTest("192.168.19.218", 8, 16, 6, 12, ""),
		staticNodeBaseResourceNodeForTest("192.168.19.219", 32, 64, 24, 48, "NVIDIA_Tesla_T4"),
	}})

	nodes, err := client.ListNodes(context.Background(), staticNodeClusterForTest())

	require.NoError(t, err)
	require.Len(t, nodes, 2)
	assert.Equal(t, "192.168.19.218", nodes[0].ID)
	assert.Empty(t, nodes[0].Status.Devices)
	assert.Equal(t, float64(8), nodes[0].Status.Allocatable.CPU)
	assert.Equal(t, float64(6), nodes[0].Status.Available.CPU)
	assert.Equal(t, "192.168.19.219", nodes[1].ID)
	require.Len(t, nodes[1].Status.Devices, 1)
}

func TestStaticNodeResourceClientRequiresFullDeviceSnapshotCoverage(t *testing.T) {
	client := newStaticNodeResourceClientForTest([]*v1.StaticNode{
		{
			Metadata: &v1.Metadata{Name: "head-0"},
			Spec:     &v1.StaticNodeSpec{IP: "192.168.19.218"},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
					Devices: []v1.StaticNodeAcceleratorDeviceStatus{
						{UUID: "GPU-abc", ProductModel: "NVIDIA_Tesla_T4", MemoryMiB: 15360, Healthy: true},
					},
				},
			},
		},
		{
			Metadata: &v1.Metadata{Name: "worker-0"},
			Spec:     &v1.StaticNodeSpec{IP: "192.168.19.219"},
			Status:   &v1.StaticNodeStatus{},
		},
	}, nil)

	nodes, err := client.ListNodes(context.Background(), staticNodeClusterForTest())

	require.ErrorIs(t, err, ErrIncompleteStaticNodeDeviceSnapshots)
	assert.Nil(t, nodes)
}

func TestStaticNodeResourceClientReturnsBaseResourcesWhenStaticNodesDoNotExist(t *testing.T) {
	client := newStaticNodeResourceClientForTest(nil, staticNodeBaseClient{nodes: []ResourceNode{
		staticNodeBaseResourceNodeForTest("192.168.19.218", 32, 64, 24, 48, "NVIDIA_Tesla_T4"),
		staticNodeBaseResourceNodeForTest("192.168.19.219", 16, 32, 12, 24, "NVIDIA_Tesla_T4"),
	}})

	nodes, err := client.ListNodes(context.Background(), staticNodeClusterForTest())

	require.NoError(t, err)
	require.Len(t, nodes, 2)
	assert.Equal(t, "192.168.19.218", nodes[0].ID)
	assert.Empty(t, nodes[0].Status.Devices)
	assert.Equal(t, float64(32), nodes[0].Status.Allocatable.CPU)
	assert.Equal(t, "192.168.19.219", nodes[1].ID)
	assert.Empty(t, nodes[1].Status.Devices)
	assert.Equal(t, float64(16), nodes[1].Status.Allocatable.CPU)
}

func TestStaticNodeResourceClientDoesNotFallbackForIncompleteDeviceSnapshots(t *testing.T) {
	client := newStaticNodeResourceClientForTest([]*v1.StaticNode{
		{
			Metadata: &v1.Metadata{Name: "head-0"},
			Spec:     &v1.StaticNodeSpec{IP: "192.168.19.218"},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
					Devices: []v1.StaticNodeAcceleratorDeviceStatus{
						{UUID: "GPU-abc", ProductModel: "NVIDIA_Tesla_T4", MemoryMiB: 15360, Healthy: true},
					},
				},
			},
		},
		{
			Metadata: &v1.Metadata{Name: "worker-0"},
			Spec:     &v1.StaticNodeSpec{IP: "192.168.19.219"},
			Status:   &v1.StaticNodeStatus{},
		},
	}, staticNodeBaseClient{nodes: []ResourceNode{
		staticNodeBaseResourceNodeForTest("192.168.19.218", 32, 64, 24, 48, "NVIDIA_Tesla_T4"),
		staticNodeBaseResourceNodeForTest("192.168.19.219", 16, 32, 12, 24, "NVIDIA_Tesla_T4"),
	}})

	nodes, err := client.ListNodes(context.Background(), staticNodeClusterForTest())

	require.ErrorIs(t, err, ErrIncompleteStaticNodeDeviceSnapshots)
	assert.Nil(t, nodes)
}

func TestStaticNodeResourceClientReturnsBaseResourceClientError(t *testing.T) {
	expectedErr := errors.New("ray nodes")
	client := newStaticNodeResourceClientForTest([]*v1.StaticNode{
		{
			Metadata: &v1.Metadata{Name: "head-0"},
			Spec:     &v1.StaticNodeSpec{IP: "192.168.19.218"},
			Status: &v1.StaticNodeStatus{
				Accelerator: &v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
					Devices: []v1.StaticNodeAcceleratorDeviceStatus{
						{UUID: "GPU-abc", ProductModel: "NVIDIA_Tesla_T4", MemoryMiB: 15360, Healthy: true},
					},
				},
			},
		},
	}, staticNodeBaseClient{err: expectedErr})

	nodes, err := client.ListNodes(context.Background(), staticNodeClusterForTest())

	require.ErrorIs(t, err, expectedErr)
	assert.Nil(t, nodes)
}

func intPtr(value int) *int {
	return &value
}
