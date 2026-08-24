package allocation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
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
		PodResources: PodResourceListerFunc(func(_ context.Context) ([]adapter.PodResource, error) {
			return []adapter.PodResource{
				{
					Namespace: "default",
					Name:      "chat-pod",
					Containers: []adapter.ContainerDevices{
						{
							ResourceName: "nvidia.com/gpu",
							DeviceIDs:    []string{"0", "GPU-def", "not-a-known-device"},
						},
					},
				},
				{
					Namespace: "default",
					Name:      "remote-pod",
					Containers: []adapter.ContainerDevices{
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

func TestKubernetesAllocationProviderBuildsRawAcceleratorEvidence(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	endpointPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "chat-pod",
			UID:       "pod-uid",
			Labels: map[string]string{
				"app":      endpointWorkloadType,
				"endpoint": "chat",
			},
			Annotations: map[string]string{"hami.io/devices-allocated": "raw"},
		},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
	provider := KubernetesAllocationProvider{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(endpointPod).
			WithIndex(&corev1.Pod{}, "spec.nodeName", podNodeNameIndex).
			Build(),
		NodeName: "node-a",
		PodResources: PodResourceListerFunc(func(_ context.Context) ([]adapter.PodResource, error) {
			return []adapter.PodResource{{
				Namespace: "default",
				Name:      "chat-pod",
				Containers: []adapter.ContainerDevices{{
					ResourceName: "vendor.example/accelerator",
					DeviceIDs:    []string{"device-0"},
				}},
			}}, nil
		}),
	}

	evidence, err := provider.KubernetesAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.True(t, evidence.AllocationAvailable)
	require.Len(t, evidence.PodResources, 1)
	require.Len(t, evidence.EndpointPods, 1)
	assert.Equal(t, "pod-uid", evidence.EndpointPods[0].UID)
	assert.Equal(t, "chat", evidence.EndpointPods[0].Labels["endpoint"])
	assert.Equal(t, "raw", evidence.EndpointPods[0].Annotations["hami.io/devices-allocated"])
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

func TestRayServeAllocationProviderBuildsStaticAcceleratorEvidence(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			actors: map[string]dashboard.Actor{
				"actor-a": {
					ActorID: "actor-a",
					NodeID:  "node-a",
					PID:     1234,
					RequiredResources: map[string]float64{
						"NPU": 1,
					},
				},
				"remote": {ActorID: "remote", NodeID: "node-b", PID: 5678},
			},
		},
		NodeIP: "10.0.0.10",
		ProcEnv: ProcessEnvReaderFunc(func(pid int) (map[string]string, error) {
			require.Equal(t, 1234, pid)

			return map[string]string{"ASCEND_VISIBLE_DEVICES": "0"}, nil
		}),
		ProcessDescendants: ProcessDescendantReaderFunc(func(pid int) ([]int, error) {
			require.Equal(t, 1234, pid)

			return []int{2345, 1234}, nil
		}),
	}

	evidence, err := provider.StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.True(t, evidence.AllocationAvailable)
	require.Len(t, evidence.RayEvidence.Actors, 1)
	assert.Equal(t, "actor-a", evidence.RayEvidence.Actors[0].ActorID)
	assert.Equal(t, 1.0, evidence.RayEvidence.Actors[0].RequiredResources["NPU"])
	assert.Empty(t, evidence.RayEvidence.Replicas)
	assert.Equal(t, 1234, evidence.RayEvidence.ActorProcesses[1234].PID)
	assert.Equal(t, []int{1234, 2345}, evidence.RayEvidence.ActorProcesses[1234].DescendantPIDs)
	assert.Equal(t, "0", evidence.RayEvidence.ActorProcesses[1234].Environment["ASCEND_VISIBLE_DEVICES"])
}

func TestRayServeAllocationProviderKeepsEvidenceWhenOneActorProcessIsUnavailable(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			actors: map[string]dashboard.Actor{
				"healthy": {ActorID: "healthy", NodeID: "node-a", PID: 1234},
				"gone":    {ActorID: "gone", NodeID: "node-a", PID: 5678},
			},
		},
		NodeIP: "10.0.0.10",
		ProcEnv: ProcessEnvReaderFunc(func(pid int) (map[string]string, error) {
			if pid == 5678 {
				return nil, errors.New("process disappeared")
			}

			return map[string]string{"ASCEND_VISIBLE_DEVICES": "0"}, nil
		}),
		ProcessDescendants: ProcessDescendantReaderFunc(func(pid int) ([]int, error) {
			return []int{pid}, nil
		}),
	}

	evidence, err := provider.StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.True(t, evidence.AllocationAvailable)
	assert.Contains(t, evidence.RayEvidence.ActorProcesses, 1234)
	assert.Contains(t, evidence.RayEvidence.ActorProcesses, 5678)
	assert.Empty(t, evidence.RayEvidence.ActorProcesses[5678].Environment)
	assert.Equal(t, []int{5678}, evidence.RayEvidence.ActorProcesses[5678].DescendantPIDs)
}

func TestRayServeAllocationProviderKeepsEvidenceWhenOnlyActorProcessIsUnavailable(t *testing.T) {
	provider := RayServeAllocationProvider{
		Dashboard: &fakeRayDashboardService{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			actors: map[string]dashboard.Actor{
				"gone": {ActorID: "gone", NodeID: "node-a", PID: 5678},
			},
		},
		NodeIP: "10.0.0.10",
		ProcEnv: ProcessEnvReaderFunc(func(int) (map[string]string, error) {
			return nil, errors.New("process disappeared")
		}),
		ProcessDescendants: ProcessDescendantReaderFunc(func(pid int) ([]int, error) {
			return []int{pid}, nil
		}),
	}

	evidence, err := provider.StaticAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.True(t, evidence.AllocationAvailable)
	require.Len(t, evidence.RayEvidence.Actors, 1)
	require.Contains(t, evidence.RayEvidence.ActorProcesses, 5678)
	assert.Empty(t, evidence.RayEvidence.ActorProcesses[5678].Environment)
	assert.Equal(t, []int{5678}, evidence.RayEvidence.ActorProcesses[5678].DescendantPIDs)
}

func TestProcFSProcessTreeReaderFindsDescendantPIDs(t *testing.T) {
	root := t.TempDir()
	writeProcStatus := func(pid, parentPID int) {
		directory := filepath.Join(root, strconv.Itoa(pid))
		require.NoError(t, os.MkdirAll(directory, 0o755))
		contents := "Name:\ttest\nPPid:\t" + strconv.Itoa(parentPID) + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(directory, "status"), []byte(contents), 0o600))
	}

	writeProcStatus(100, 1)
	writeProcStatus(200, 100)
	writeProcStatus(300, 200)
	writeProcStatus(400, 1)

	pids, err := (ProcFSProcessTreeReader{Root: root}).DescendantPIDs(100)

	require.NoError(t, err)
	assert.Equal(t, []int{100, 200, 300}, pids)
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
	nodeID := ""
	for _, filter := range filters {
		if filter.Key == "actor_id" && filter.Predicate == "=" {
			actorID = filter.Value
		}
		if filter.Key == "node_id" && filter.Predicate == "=" {
			nodeID = filter.Value
		}
	}

	if nodeID != "" {
		actors := make([]dashboard.Actor, 0)
		for _, actor := range f.actors {
			if actor.NodeID == nodeID {
				actors = append(actors, actor)
			}
		}

		return &dashboard.ActorsResponse{
			Result: true,
			Data: dashboard.ActorsResponseData{
				Result: dashboard.ActorsListResult{Result: actors},
			},
		}, nil
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

func podNodeNameIndex(object client.Object) []string {
	pod, ok := object.(*corev1.Pod)
	if !ok || pod.Spec.NodeName == "" {
		return nil
	}

	return []string{pod.Spec.NodeName}
}
