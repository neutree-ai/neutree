package allocation

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

func TestMultiProviderMergesAllocations(t *testing.T) {
	snapshot := &v1.NodeDeviceSnapshot{}
	provider := MultiProvider{
		Providers: []Provider{
			ProviderFunc(func(_ context.Context, got *v1.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
				require.Same(t, snapshot, got)

				return []v1.StaticNodeAllocationStatus{{Endpoint: "chat", InstanceID: "pod-a"}}, nil
			}),
			ProviderFunc(func(_ context.Context, got *v1.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
				require.Same(t, snapshot, got)

				return []v1.StaticNodeAllocationStatus{{Endpoint: "embed", InstanceID: "pod-b"}}, nil
			}),
		},
	}

	allocations, err := provider.Allocations(context.Background(), snapshot)

	require.NoError(t, err)
	require.Len(t, allocations, 2)
	assert.Equal(t, "chat", allocations[0].Endpoint)
	assert.Equal(t, "embed", allocations[1].Endpoint)
}

func TestMultiProviderReturnsProviderError(t *testing.T) {
	expectedErr := errors.New("boom")
	provider := MultiProvider{
		Providers: []Provider{
			ProviderFunc(func(_ context.Context, _ *v1.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
				return nil, expectedErr
			}),
		},
	}

	allocations, err := provider.Allocations(context.Background(), &v1.NodeDeviceSnapshot{})

	require.ErrorIs(t, err, expectedErr)
	assert.Nil(t, allocations)
}

func TestKubernetesAllocationProviderMapsPodResourcesToExactDeviceUUIDs(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	kubernetesClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "chat-pod",
					Labels: map[string]string{
						"endpoint":                         "chat",
						v1.NeutreeClusterLabelKey:          "cluster-a",
						v1.NeutreeClusterWorkspaceLabelKey: "default",
					},
				},
				Spec: corev1.PodSpec{NodeName: "node-a"},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "remote-pod",
					Labels: map[string]string{
						"endpoint":                         "remote",
						v1.NeutreeClusterWorkspaceLabelKey: "default",
					},
				},
				Spec: corev1.PodSpec{NodeName: "node-b"},
			},
		).
		Build()
	provider := KubernetesAllocationProvider{
		Client:   kubernetesClient,
		NodeName: "node-a",
		PodResources: PodResourceListerFunc(func(_ context.Context) ([]model.PodResource, error) {
			return []model.PodResource{
				{
					Namespace: "default",
					Name:      "chat-pod",
					Containers: []model.ContainerDevices{
						{
							ResourceName: "nvidia.com/gpu",
							DeviceIDs:    []string{"0", "GPU-def", "not-a-known-device"},
						},
					},
				},
				{
					Namespace: "default",
					Name:      "remote-pod",
					Containers: []model.ContainerDevices{
						{ResourceName: "nvidia.com/gpu", DeviceIDs: []string{"GPU-remote"}},
					},
				},
			}, nil
		}),
	}
	snapshot := &v1.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
				{ID: "1", UUID: "GPU-def", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
	}

	allocations, err := provider.Allocations(context.Background(), snapshot)

	require.NoError(t, err)
	require.Len(t, allocations, 1)
	assert.Equal(t, "endpoint", allocations[0].WorkloadType)
	assert.Equal(t, "default", allocations[0].Workspace)
	assert.Equal(t, "chat", allocations[0].Endpoint)
	assert.Equal(t, "chat-pod", allocations[0].InstanceID)
	assert.Equal(t, "chat-pod", allocations[0].ReplicaID)
	assert.Equal(t, "default/chat-pod", allocations[0].RuntimeID)
	require.Len(t, allocations[0].Devices, 2)
	assert.Equal(t, "GPU-abc", allocations[0].Devices[0].UUID)
	assert.Equal(t, "GPU-def", allocations[0].Devices[1].UUID)
	assert.Equal(t, int64(81920), allocations[0].Devices[0].MemoryMiB)
	assert.Equal(t, int64(100), allocations[0].Devices[0].CoreUnits)
}

func TestRayServeAllocationProviderMapsActorProcessVisibleDevices(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			applications: &dashboard.RayServeApplicationsResponse{
				Applications: map[string]dashboard.RayServeApplicationStatus{
					"default_chat": {
						Status: dashboard.ApplicationStatusRunning,
						Deployments: map[string]dashboard.Deployment{
							"Backend": {
								Name: "Backend",
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "actor-a", ReplicaID: "replica-a"},
								},
							},
						},
					},
				},
			},
			actors: map[string]dashboard.Actor{
				"actor-a": {ActorID: "actor-a", PID: 1234},
			},
		},
		NodeIP: "10.0.0.10",
		Node:   "head-0",
		ProcEnv: ProcessEnvReaderFunc(func(pid int) (map[string]string, error) {
			require.Equal(t, 1234, pid)

			return map[string]string{"CUDA_VISIBLE_DEVICES": "0"}, nil
		}),
	}
	snapshot := &v1.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
	}

	allocations, err := provider.Allocations(context.Background(), snapshot)

	require.NoError(t, err)
	require.Len(t, allocations, 1)
	assert.Equal(t, "endpoint", allocations[0].WorkloadType)
	assert.Equal(t, "default", allocations[0].Workspace)
	assert.Equal(t, "chat", allocations[0].Endpoint)
	assert.Equal(t, "actor-a", allocations[0].InstanceID)
	assert.Equal(t, "replica-a", allocations[0].ReplicaID)
	assert.Equal(t, "actor-a", allocations[0].RuntimeID)
	assert.Equal(t, 1234, allocations[0].PID)
	require.Len(t, allocations[0].Devices, 1)
	assert.Equal(t, "GPU-abc", allocations[0].Devices[0].UUID)
	assert.Equal(t, "NVIDIA_A100", allocations[0].Devices[0].Product)
	assert.Equal(t, "head-0", allocations[0].Devices[0].NodeID)
}

func TestRayServeAllocationProviderScalesFractionalGPUAllocation(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			applications: &dashboard.RayServeApplicationsResponse{
				Applications: map[string]dashboard.RayServeApplicationStatus{
					"default_chat": {
						Status: dashboard.ApplicationStatusRunning,
						DeployedAppConfig: &dashboard.RayServeApplication{
							Args: map[string]interface{}{
								"deployment_options": map[string]interface{}{
									"backend": map[string]interface{}{
										"num_gpus": 0.5,
									},
								},
							},
						},
						Deployments: map[string]dashboard.Deployment{
							"Backend": {
								Name: "Backend",
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "actor-a", ReplicaID: "replica-a"},
								},
							},
						},
					},
				},
			},
			actors: map[string]dashboard.Actor{
				"actor-a": {ActorID: "actor-a", PID: 1234},
			},
		},
		NodeIP: "10.0.0.10",
		Node:   "head-0",
		ProcEnv: ProcessEnvReaderFunc(func(_ int) (map[string]string, error) {
			return map[string]string{"CUDA_VISIBLE_DEVICES": "0"}, nil
		}),
	}
	snapshot := &v1.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
	}

	allocations, err := provider.Allocations(context.Background(), snapshot)

	require.NoError(t, err)
	require.Len(t, allocations, 1)
	require.Len(t, allocations[0].Devices, 1)
	assert.Equal(t, "GPU-abc", allocations[0].Devices[0].UUID)
	assert.Equal(t, int64(40960), allocations[0].Devices[0].MemoryMiB)
	assert.Equal(t, int64(50), allocations[0].Devices[0].CoreUnits)
}

func TestRayServeAllocationProviderDistributesFractionalGPUQuantityAcrossVisibleDevices(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			applications: &dashboard.RayServeApplicationsResponse{
				Applications: map[string]dashboard.RayServeApplicationStatus{
					"default_chat": {
						Status: dashboard.ApplicationStatusRunning,
						DeployedAppConfig: &dashboard.RayServeApplication{
							Args: map[string]interface{}{
								"deployment_options": map[string]interface{}{
									"backend": map[string]interface{}{
										"num_gpus": 1.5,
									},
								},
							},
						},
						Deployments: map[string]dashboard.Deployment{
							"Backend": {
								Name: "Backend",
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "actor-a", ReplicaID: "replica-a"},
								},
							},
						},
					},
				},
			},
			actors: map[string]dashboard.Actor{
				"actor-a": {ActorID: "actor-a", PID: 1234},
			},
		},
		NodeIP: "10.0.0.10",
		Node:   "head-0",
		ProcEnv: ProcessEnvReaderFunc(func(_ int) (map[string]string, error) {
			return map[string]string{"CUDA_VISIBLE_DEVICES": "0,1"}, nil
		}),
	}
	snapshot := &v1.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
				{ID: "1", UUID: "GPU-def", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
	}

	allocations, err := provider.Allocations(context.Background(), snapshot)

	require.NoError(t, err)
	require.Len(t, allocations, 1)
	require.Len(t, allocations[0].Devices, 2)
	assert.Equal(t, "GPU-abc", allocations[0].Devices[0].UUID)
	assert.Equal(t, int64(81920), allocations[0].Devices[0].MemoryMiB)
	assert.Equal(t, int64(100), allocations[0].Devices[0].CoreUnits)
	assert.Equal(t, "GPU-def", allocations[0].Devices[1].UUID)
	assert.Equal(t, int64(40960), allocations[0].Devices[1].MemoryMiB)
	assert.Equal(t, int64(50), allocations[0].Devices[1].CoreUnits)
}

func TestRayServeAllocationProviderSkipsExplicitZeroGPUDeployment(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			applications: &dashboard.RayServeApplicationsResponse{
				Applications: map[string]dashboard.RayServeApplicationStatus{
					"default_chat": {
						Status: dashboard.ApplicationStatusRunning,
						DeployedAppConfig: &dashboard.RayServeApplication{
							Args: map[string]interface{}{
								"deployment_options": map[string]interface{}{
									"backend": map[string]interface{}{
										"num_gpus": 0,
									},
								},
							},
						},
						Deployments: map[string]dashboard.Deployment{
							"Backend": {
								Name: "Backend",
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "actor-a", ReplicaID: "replica-a"},
								},
							},
						},
					},
				},
			},
			actors: map[string]dashboard.Actor{
				"actor-a": {ActorID: "actor-a", PID: 1234},
			},
		},
		NodeIP: "10.0.0.10",
		Node:   "head-0",
		ProcEnv: ProcessEnvReaderFunc(func(_ int) (map[string]string, error) {
			return map[string]string{"CUDA_VISIBLE_DEVICES": "0"}, nil
		}),
	}
	snapshot := &v1.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
	}

	allocations, err := provider.Allocations(context.Background(), snapshot)

	require.NoError(t, err)
	assert.Empty(t, allocations)
}

func TestRayServeAllocationProviderPrefersExactNVIDIAUUIDOverRelativeCUDAIndex(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			applications: &dashboard.RayServeApplicationsResponse{
				Applications: map[string]dashboard.RayServeApplicationStatus{
					"default_chat": {
						Status: dashboard.ApplicationStatusRunning,
						Deployments: map[string]dashboard.Deployment{
							"Backend": {
								Name: "Backend",
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "actor-a", ReplicaID: "replica-a"},
								},
							},
						},
					},
				},
			},
			actors: map[string]dashboard.Actor{
				"actor-a": {ActorID: "actor-a", PID: 1234},
			},
		},
		NodeIP: "10.0.0.10",
		Node:   "head-0",
		ProcEnv: ProcessEnvReaderFunc(func(_ int) (map[string]string, error) {
			return map[string]string{
				"CUDA_VISIBLE_DEVICES":   "0",
				"NVIDIA_VISIBLE_DEVICES": "GPU-def",
			}, nil
		}),
	}
	snapshot := &v1.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
				{ID: "1", UUID: "GPU-def", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
	}

	allocations, err := provider.Allocations(context.Background(), snapshot)

	require.NoError(t, err)
	require.Len(t, allocations, 1)
	require.Len(t, allocations[0].Devices, 1)
	assert.Equal(t, "GPU-def", allocations[0].Devices[0].UUID)
}

func TestRayServeAllocationProviderIgnoresAmbiguousAllVisibleDevices(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			applications: &dashboard.RayServeApplicationsResponse{
				Applications: map[string]dashboard.RayServeApplicationStatus{
					"default_chat": {
						Status: dashboard.ApplicationStatusRunning,
						Deployments: map[string]dashboard.Deployment{
							"Backend": {
								Name: "Backend",
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "backend-actor", ReplicaID: "backend-replica"},
								},
							},
							"Controller": {
								Name: "Controller",
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "controller-actor", ReplicaID: "controller-replica"},
								},
							},
						},
					},
				},
			},
			actors: map[string]dashboard.Actor{
				"backend-actor":    {ActorID: "backend-actor", PID: 2000},
				"controller-actor": {ActorID: "controller-actor", PID: 1000},
			},
		},
		NodeIP: "10.0.0.10",
		Node:   "head-0",
		ProcEnv: ProcessEnvReaderFunc(func(pid int) (map[string]string, error) {
			if pid == 1000 {
				return map[string]string{"NVIDIA_VISIBLE_DEVICES": "all"}, nil
			}

			return map[string]string{"NVIDIA_VISIBLE_DEVICES": "void"}, nil
		}),
	}
	snapshot := &v1.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
				{ID: "1", UUID: "GPU-def", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
	}

	allocations, err := provider.Allocations(context.Background(), snapshot)

	require.NoError(t, err)
	require.Empty(t, allocations)
}

func TestRayServeAllocationProviderMapsDescendantGPUProcess(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			applications: &dashboard.RayServeApplicationsResponse{
				Applications: map[string]dashboard.RayServeApplicationStatus{
					"default_chat": {
						Status: dashboard.ApplicationStatusRunning,
						Deployments: map[string]dashboard.Deployment{
							"Backend": {
								Name: "Backend",
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "backend-actor", ReplicaID: "backend-replica"},
								},
							},
							"Controller": {
								Name: "Controller",
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "controller-actor", ReplicaID: "controller-replica"},
								},
							},
						},
					},
				},
			},
			actors: map[string]dashboard.Actor{
				"backend-actor":    {ActorID: "backend-actor", PID: 2000},
				"controller-actor": {ActorID: "controller-actor", PID: 1000},
			},
		},
		NodeIP: "10.0.0.10",
		Node:   "head-0",
		ProcEnv: ProcessEnvReaderFunc(func(pid int) (map[string]string, error) {
			if pid == 1000 {
				return map[string]string{"NVIDIA_VISIBLE_DEVICES": "all"}, nil
			}

			return map[string]string{"NVIDIA_VISIBLE_DEVICES": "void"}, nil
		}),
		GPUProcesses: GPUProcessReaderFunc(func(_ context.Context) ([]GPUProcess, error) {
			return []GPUProcess{{UUID: "GPU-abc", PID: 3000, UsedMemoryMiB: 4096}}, nil
		}),
		ProcessTree: ProcessTreeReaderFunc(func(pid, ancestorPID int) (bool, error) {
			return pid == 3000 && ancestorPID == 2000, nil
		}),
	}
	snapshot := &v1.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
				{ID: "1", UUID: "GPU-def", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
	}

	allocations, err := provider.Allocations(context.Background(), snapshot)

	require.NoError(t, err)
	require.Len(t, allocations, 1)
	assert.Equal(t, "backend-actor", allocations[0].InstanceID)
	assert.Equal(t, "backend-replica", allocations[0].ReplicaID)
	assert.Equal(t, 2000, allocations[0].PID)
	require.Len(t, allocations[0].Devices, 1)
	assert.Equal(t, "GPU-abc", allocations[0].Devices[0].UUID)
	assert.Equal(t, int64(4096), allocations[0].Devices[0].UsedMemoryMiB)
}

func TestRayServeAllocationProviderKeepsEnvVisibleDevicesWhenOnlyOneGPUHasProcess(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			applications: &dashboard.RayServeApplicationsResponse{
				Applications: map[string]dashboard.RayServeApplicationStatus{
					"default_chat": {
						Status: dashboard.ApplicationStatusRunning,
						Deployments: map[string]dashboard.Deployment{
							"Backend": {
								Name: "Backend",
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "actor-a", ReplicaID: "replica-a"},
								},
							},
						},
					},
				},
			},
			actors: map[string]dashboard.Actor{
				"actor-a": {ActorID: "actor-a", PID: 2000},
			},
		},
		NodeIP: "10.0.0.10",
		Node:   "head-0",
		ProcEnv: ProcessEnvReaderFunc(func(_ int) (map[string]string, error) {
			return map[string]string{"CUDA_VISIBLE_DEVICES": "0,1"}, nil
		}),
		GPUProcesses: GPUProcessReaderFunc(func(_ context.Context) ([]GPUProcess, error) {
			return []GPUProcess{{UUID: "GPU-abc", PID: 3000, UsedMemoryMiB: 4096}}, nil
		}),
		ProcessTree: ProcessTreeReaderFunc(func(pid, ancestorPID int) (bool, error) {
			return pid == 3000 && ancestorPID == 2000, nil
		}),
	}
	snapshot := &v1.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
				{ID: "1", UUID: "GPU-def", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
	}

	allocations, err := provider.Allocations(context.Background(), snapshot)

	require.NoError(t, err)
	require.Len(t, allocations, 1)
	require.Len(t, allocations[0].Devices, 2)
	assert.Equal(t, "GPU-abc", allocations[0].Devices[0].UUID)
	assert.Equal(t, int64(4096), allocations[0].Devices[0].UsedMemoryMiB)
	assert.Equal(t, "GPU-def", allocations[0].Devices[1].UUID)
	assert.Equal(t, int64(0), allocations[0].Devices[1].UsedMemoryMiB)
}

func TestRayServeAllocationProviderUsesRoutePrefixForEndpointIdentity(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			applications: &dashboard.RayServeApplicationsResponse{
				Applications: map[string]dashboard.RayServeApplicationStatus{
					"default_work_space_chat_model": {
						Status: dashboard.ApplicationStatusRunning,
						DeployedAppConfig: &dashboard.RayServeApplication{
							RoutePrefix: "/default_work_space/chat_model",
						},
						Deployments: map[string]dashboard.Deployment{
							"Backend": {
								Name: "Backend",
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "actor-a", ReplicaID: "replica-a"},
								},
							},
						},
					},
				},
			},
			actors: map[string]dashboard.Actor{
				"actor-a": {ActorID: "actor-a", PID: 1234},
			},
		},
		NodeIP: "10.0.0.10",
		ProcEnv: ProcessEnvReaderFunc(func(_ int) (map[string]string, error) {
			return map[string]string{"CUDA_VISIBLE_DEVICES": "0"}, nil
		}),
	}
	snapshot := &v1.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
			},
		},
	}

	allocations, err := provider.Allocations(context.Background(), snapshot)

	require.NoError(t, err)
	require.Len(t, allocations, 1)
	assert.Equal(t, "default_work_space", allocations[0].Workspace)
	assert.Equal(t, "chat_model", allocations[0].Endpoint)
}

func TestParseNvidiaSMIComputeProcessesIncludesUsedMemory(t *testing.T) {
	processes := parseNvidiaSMIComputeProcesses(`GPU-abc, 3000, 4096
GPU-def, 4000, 512 MiB
GPU-skip, not-a-pid, 128
`)

	require.Len(t, processes, 2)
	assert.Equal(t, GPUProcess{UUID: "GPU-abc", PID: 3000, UsedMemoryMiB: 4096}, processes[0])
	assert.Equal(t, GPUProcess{UUID: "GPU-def", PID: 4000, UsedMemoryMiB: 512}, processes[1])
}

type fakeRayDashboardService struct {
	nodes        []v1.NodeSummary
	applications *dashboard.RayServeApplicationsResponse
	actors       map[string]dashboard.Actor
}

func (f *fakeRayDashboardService) GetClusterMetadata() (*dashboard.ClusterMetadataResponse, error) {
	return &dashboard.ClusterMetadataResponse{}, nil
}

func (f *fakeRayDashboardService) ListNodes() ([]v1.NodeSummary, error) {
	return f.nodes, nil
}

func (f *fakeRayDashboardService) GetClusterStatus() (v1.RayAPIClusterStatus, error) {
	return v1.RayAPIClusterStatus{}, nil
}

func (f *fakeRayDashboardService) GetServeApplications() (*dashboard.RayServeApplicationsResponse, error) {
	return f.applications, nil
}

func (f *fakeRayDashboardService) UpdateServeApplications(_ dashboard.RayServeApplicationsRequest) error {
	return nil
}

func (f *fakeRayDashboardService) GetActorLog(_, _ string, _ int) (string, error) {
	return "", nil
}

func (f *fakeRayDashboardService) ListActors(
	filters []dashboard.ActorFilter,
	_ bool,
	_ int,
) (*dashboard.ActorsResponse, error) {
	actorID := ""
	for _, filter := range filters {
		if filter.Key == "actor_id" && filter.Predicate == "=" {
			actorID = filter.Value
		}
	}

	actor, ok := f.actors[actorID]
	if !ok {
		return nil, errors.New("actor not found")
	}

	return &dashboard.ActorsResponse{
		Result: true,
		Data: dashboard.ActorsResponseData{
			Result: dashboard.ActorsListResult{
				Result: []dashboard.Actor{actor},
			},
		},
	}, nil
}
