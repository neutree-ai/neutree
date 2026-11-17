package resource

import (
	"context"
	"errors"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/stretchr/testify/assert"
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

type fakeRayLogDashboard struct {
	dashboard.DashboardService
	calls int
	out   string
	err   string
	get   func(ctx context.Context, actorID, suffix string, maxBytes int64) (string, error)
}

func (f *fakeRayLogDashboard) GetActorLogHead(
	ctx context.Context,
	actorID, suffix string,
	maxBytes int64,
) (string, error) {
	f.calls++
	if f.get != nil {
		return f.get(ctx, actorID, suffix, maxBytes)
	}

	if suffix == "err" {
		return f.err, nil
	}

	return f.out, nil
}

func TestStaticNodeResourceClientCollectsVLLMStartupMemory(t *testing.T) {
	node := &v1.StaticNode{
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
	}

	cluster, lister := staticNodeClusterAndListerForTest([]*v1.StaticNode{node})
	endpoint := testEndpoint("chat", "NVIDIA-L20")
	endpoint.Spec.Engine = &v1.EndpointEngineSpec{Engine: v1.EngineNameVLLM}
	dash := &fakeRayLogDashboard{
		out: "Model loading took 10.5 GiB\n",
		err: "Available KV cache memory: 4.25 GiB\n",
	}

	// Non-running actors are not queried yet and must not lock empty cache state.
	idleClient := NewStaticNodeClusterResourceClientWithDashboard(lister, nil, dash, map[string]struct{}{})
	idleStatus, err := NewResourceViewBuilder(idleClient).BuildEndpointResources(context.Background(), cluster, endpoint)
	require.NoError(t, err)
	require.Len(t, idleStatus.Replicas, 1)
	require.Equal(t, 0, dash.calls)
	require.Empty(t, idleStatus.Replicas[0].ContainerID)
	require.Nil(t, idleStatus.Replicas[0].ModelWeightsMiB)

	// Running actor populates weights, KV cache, and actor ContainerID from startup head logs.
	client := NewStaticNodeClusterResourceClientWithDashboard(lister, nil, dash, map[string]struct{}{"actor-1": {}})
	status, err := NewResourceViewBuilder(client).BuildEndpointResources(context.Background(), cluster, endpoint)
	require.NoError(t, err)
	require.Len(t, status.Replicas, 1)
	require.Equal(t, 2, dash.calls)
	require.Equal(t, "actor-1", status.Replicas[0].ContainerID)
	require.NotNil(t, status.Replicas[0].ModelWeightsMiB)
	require.InDelta(t, 10752.0, *status.Replicas[0].ModelWeightsMiB, 0.01)
	require.NotNil(t, status.Replicas[0].KVCacheCapacityMiB)
	require.InDelta(t, 4352.0, *status.Replicas[0].KVCacheCapacityMiB, 0.01)

	// Subsequent reconciles reuse cached values without re-reading logs (even while draining).
	endpoint.Status = &v1.EndpointStatus{Resources: status}
	for _, running := range []map[string]struct{}{{"actor-1": {}}, {}} {
		c := NewStaticNodeClusterResourceClientWithDashboard(lister, nil, dash, running)
		cachedStatus, err := NewResourceViewBuilder(c).BuildEndpointResources(context.Background(), cluster, endpoint)
		require.NoError(t, err)
		require.Equal(t, 2, dash.calls)
		require.Equal(t, "actor-1", cachedStatus.Replicas[0].ContainerID)
		require.Equal(t, status.Replicas[0].ModelWeightsMiB, cachedStatus.Replicas[0].ModelWeightsMiB)
		require.Equal(t, status.Replicas[0].KVCacheCapacityMiB, cachedStatus.Replicas[0].KVCacheCapacityMiB)
	}

	// num_gpu_blocks_override clears KV cache capacity for both cached and uncached reads.
	for _, key := range []string{"num_gpu_blocks_override", "num-gpu-blocks-override"} {
		endpoint.Spec.Variables = map[string]interface{}{"engine_args": map[string]interface{}{key: 128}}
		for _, cached := range []bool{true, false} {
			endpoint.Status.Resources = status
			if !cached {
				endpoint.Status.Resources = nil
			}
			got, err := NewResourceViewBuilder(client).BuildEndpointResources(context.Background(), cluster, endpoint)
			require.NoError(t, err)
			require.NotNil(t, got.Replicas[0].ModelWeightsMiB)
			require.InDelta(t, 10752.0, *got.Replicas[0].ModelWeightsMiB, 0.01)
			require.Nil(t, got.Replicas[0].KVCacheCapacityMiB, key)
		}
	}
	endpoint.Spec.Variables = nil

	// A successful read with no matching startup lines caches completion for this running actor.
	endpoint.Status.Resources = nil
	dash.out, dash.err = "starting up\n", ""
	calls := dash.calls
	empty, err := NewResourceViewBuilder(client).BuildEndpointResources(context.Background(), cluster, endpoint)
	require.NoError(t, err)
	require.Equal(t, calls+2, dash.calls)
	require.Equal(t, "actor-1", empty.Replicas[0].ContainerID)
	require.Nil(t, empty.Replicas[0].ModelWeightsMiB)
	require.Nil(t, empty.Replicas[0].KVCacheCapacityMiB)

	endpoint.Status.Resources = empty
	dash.out, dash.err = "Model loading took 6 GiB\n", "Available KV cache memory: 8 GiB\n"
	stillEmpty, err := NewResourceViewBuilder(client).BuildEndpointResources(context.Background(), cluster, endpoint)
	require.NoError(t, err)
	require.Equal(t, calls+2, dash.calls)
	require.Nil(t, stillEmpty.Replicas[0].ModelWeightsMiB)
	require.Nil(t, stillEmpty.Replicas[0].KVCacheCapacityMiB)

	// A new actor reads logs fresh even when the previous actor cached (nil, nil).
	lister.nodes[0].Status.Allocations[0].RuntimeID = "actor-2"
	client = NewStaticNodeClusterResourceClientWithDashboard(lister, nil, dash, map[string]struct{}{"actor-2": {}})
	filled, err := NewResourceViewBuilder(client).BuildEndpointResources(context.Background(), cluster, endpoint)
	require.NoError(t, err)
	require.Equal(t, calls+4, dash.calls)
	require.Equal(t, "actor-2", filled.Replicas[0].ContainerID)
	require.InDelta(t, 6144.0, *filled.Replicas[0].ModelWeightsMiB, 0.01)
	require.InDelta(t, 8192.0, *filled.Replicas[0].KVCacheCapacityMiB, 0.01)

	// Partial stream failure ("out" succeeds, "err" fails) must not lock partial results.
	endpoint.Status.Resources = nil
	dash.get = func(_ context.Context, _, suffix string, _ int64) (string, error) {
		if suffix == "out" {
			return "Model loading took 6 GiB\n", nil
		}

		return "", errors.New("stderr unavailable")
	}
	partialFailed, err := NewResourceViewBuilder(client).BuildEndpointResources(context.Background(), cluster, endpoint)
	require.NoError(t, err)
	require.Empty(t, partialFailed.Replicas[0].ContainerID)
	require.Nil(t, partialFailed.Replicas[0].ModelWeightsMiB)
	require.Nil(t, partialFailed.Replicas[0].KVCacheCapacityMiB)

	// Expired context skips reads without locking cache.
	endpoint.Status.Resources = partialFailed
	expiredCtx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	calls = dash.calls
	timedOut, err := NewResourceViewBuilder(client).BuildEndpointResources(expiredCtx, cluster, endpoint)
	require.NoError(t, err)
	require.Equal(t, calls, dash.calls)
	require.Empty(t, timedOut.Replicas[0].ContainerID)

	// Full failure short-circuits before stderr, stays uncached, and retries cleanly on recovery.
	dash.get = func(ctx context.Context, _, _ string, _ int64) (string, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), startupLogTimeout)

		return "", errors.New("dashboard unavailable")
	}
	calls = dash.calls
	fullFailed, err := NewResourceViewBuilder(client).BuildEndpointResources(context.Background(), cluster, endpoint)
	require.NoError(t, err)
	require.Equal(t, calls+1, dash.calls)
	require.Empty(t, fullFailed.Replicas[0].ContainerID)

	endpoint.Status.Resources = fullFailed
	dash.get = nil
	calls = dash.calls
	retried, err := NewResourceViewBuilder(client).BuildEndpointResources(context.Background(), cluster, endpoint)
	require.NoError(t, err)
	require.Equal(t, calls+2, dash.calls)
	require.Equal(t, "actor-2", retried.Replicas[0].ContainerID)
	require.NotNil(t, retried.Replicas[0].ModelWeightsMiB)
	require.NotNil(t, retried.Replicas[0].KVCacheCapacityMiB)

	// Non-vLLM engines never query actor logs.
	for _, engine := range []string{v1.EngineNameSGLang, v1.EngineNameLlamaCpp, "custom"} {
		endpoint.Spec.Engine.Engine = engine
		endpoint.Status.Resources = nil
		calls = dash.calls
		result, err := NewResourceViewBuilder(client).BuildEndpointResources(context.Background(), cluster, endpoint)
		require.NoError(t, err)
		require.Equal(t, calls, dash.calls, engine)
		require.Empty(t, result.Replicas[0].ContainerID)
		require.Nil(t, result.Replicas[0].ModelWeightsMiB)
	}
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
