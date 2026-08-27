package nvidia

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hardware"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestNvidiaAdapterDiscoversHardware(t *testing.T) {
	testCases := []struct {
		name  string
		infos []model.GPUHardwareInfo
		err   error
	}{
		{
			name: "inventory",
			infos: []model.GPUHardwareInfo{{
				UUID:              "GPU-def",
				Index:             "1",
				MinorNumber:       "3",
				Product:           "NVIDIA L20",
				Architecture:      "Ada",
				CUDACapability:    "8.9",
				DriverVersion:     "550.54",
				CUDADriverVersion: "12.8",
				MemoryTotalMiB:    "46068",
				PCIEBusID:         "00000000:05:00.0",
				PCIEGeneration:    "4",
				PCIEWidth:         "16",
				NUMANode:          "0",
			}},
		},
		{name: "provider error", err: errors.New("nvml unavailable")},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			accelerator := &nvidiaAccelerator{provider: hardware.GPUHardwareInfoProviderFunc(
				func(context.Context) ([]model.GPUHardwareInfo, error) {
					return testCase.infos, testCase.err
				},
			)}

			snapshot, err := accelerator.DiscoverHardware(context.Background())

			require.NoError(t, err)
			assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), snapshot.Accelerator.Type)
			if testCase.err != nil {
				assert.Empty(t, snapshot.Accelerator.Devices)
				assert.Empty(t, snapshot.Details)
				return
			}

			require.Len(t, snapshot.Accelerator.Devices, 1)
			assert.Equal(t, "GPU-def", snapshot.Accelerator.Devices[0].UUID)
			assert.Equal(t, int64(46068), snapshot.Accelerator.Devices[0].MemoryMiB)
			require.NotNil(t, snapshot.Accelerator.Devices[0].MinorNumber)
			assert.Equal(t, 3, *snapshot.Accelerator.Devices[0].MinorNumber)
			require.Len(t, snapshot.Details, 1)
			assert.Equal(t, "Ada", snapshot.Details[0].Architecture)
			assert.Equal(t, "8.9", snapshot.Details[0].CUDACapability)
			assert.Equal(t, "550.54", snapshot.Details[0].DriverVersion)
			assert.Equal(t, "12.8", snapshot.Details[0].CUDADriverVersion)
		})
	}
}

func TestNvidiaAdapterDeclaresNVIDIAInfoDescriptor(t *testing.T) {
	provider, ok := NewNodeAgentAdapter().(adapter.MetricDescriptorProvider)
	require.True(t, ok)

	descriptors := provider.MetricDescriptors()
	require.Len(t, descriptors, 1)
	assert.Equal(t, "neutree_node_accelerator_nvidia_info", descriptors[0].Name)
	assert.Equal(t, []string{
		"cluster_type",
		"node",
		"accelerator_type",
		"accelerator_uuid",
		"accelerator_index",
		"product",
		"architecture",
		"cuda_capability",
		"driver_version",
		"cuda_driver_version",
		"nvlink",
		"nvswitch",
	}, descriptors[0].LabelNames)
	assert.Equal(t, []string{"accelerator_uuid"}, descriptors[0].RequiredLabelNames)
}

func TestNewNodeAgentAdapterReturnsFreshInstances(t *testing.T) {
	first := NewNodeAgentAdapter()
	second := NewNodeAgentAdapter()

	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), first.Type())
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), second.Type())
	assert.NotSame(t, first, second)
}

func TestNvidiaHAMiUsesVirtualizationMonitorEvidence(t *testing.T) {
	evidence := adapter.KubernetesEvidence{
		VirtualizationMonitor: adapter.VirtualizationMonitorEvidence{Up: true, Text: `hami_vgpu_memory_used_bytes{namespace="default",pod="chat-a",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a",device_name="NVIDIA_A100"} 1024
hami_container_device_utilization_ratio{namespace="default",pod="chat-a",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a",device_name="NVIDIA_A100"} 0.75`},
		EndpointPods: []adapter.EndpointPodEvidence{{
			Namespace: "default",
			Name:      "chat-a",
			NodeName:  "node-a",
			Labels: map[string]string{
				"endpoint":                         "chat",
				v1.NeutreeClusterWorkspaceLabelKey: "workspace-a",
				v1.NeutreeClusterLabelKey:          "cluster-a",
			},
		}},
	}

	usages := nvidiaHAMiEndpointReplicaUsagesFromEvidence(evidence)

	require.Len(t, usages, 1)
	assert.Equal(t, "workspace-a", usages[0].Workspace)
	assert.Equal(t, "cluster-a", usages[0].Cluster)
	assert.Equal(t, "chat", usages[0].Endpoint)
	assert.Equal(t, "GPU-abc", usages[0].GPUUUID)
	require.NotNil(t, usages[0].MemoryUsedBytes)
	assert.Equal(t, 1024.0, *usages[0].MemoryUsedBytes)
	require.NotNil(t, usages[0].UtilizationRatio)
	assert.Equal(t, 0.75, *usages[0].UtilizationRatio)
}

func TestNvidiaHAMiSkipsUnavailableVirtualizationMonitor(t *testing.T) {
	usages := nvidiaHAMiEndpointReplicaUsagesFromEvidence(adapter.KubernetesEvidence{
		VirtualizationMonitor: adapter.VirtualizationMonitorEvidence{Text: `hami_vgpu_memory_used_bytes{namespace="default",pod="chat-a",device_uuid="GPU-abc"} 1024`},
	})

	assert.Empty(t, usages)
}

func TestNvidiaAdapterBuildsMetricsFromVirtualizationMonitorEvidence(t *testing.T) {
	result, err := (&nvidiaAccelerator{}).BuildKubernetesMetrics(
		context.Background(),
		testNvidiaHardware(),
		adapter.KubernetesEvidence{
			Common: adapter.CommonEvidence{Labels: adapter.CanonicalLabels{
				ClusterType: v1.KubernetesClusterType,
				Node:        "node-a",
			}},
			VirtualizationMonitor: adapter.VirtualizationMonitorEvidence{Up: true, Text: `hami_vgpu_memory_used_bytes{namespace="default",pod="chat-a",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a"} 1024
hami_container_device_utilization_ratio{namespace="default",pod="chat-a",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a"} 0.75`},
			EndpointPods: []adapter.EndpointPodEvidence{{
				Namespace: "default",
				Name:      "chat-a",
				NodeName:  "node-a",
				Labels:    map[string]string{"endpoint": "chat"},
			}},
		},
	)

	require.NoError(t, err)
	assert.Contains(t, sampleNames(result.Samples), "neutree_endpoint_replica_accelerator_memory_used_bytes")
	assert.Contains(t, sampleNames(result.Samples), "neutree_endpoint_replica_accelerator_utilization_ratio")
}

func TestNvidiaAdapterBuildsKubernetesAllocationsFromRawEvidence(t *testing.T) {
	accelerator := &nvidiaAccelerator{}
	result, err := accelerator.BuildKubernetesMetrics(context.Background(), testNvidiaHardware(), adapter.KubernetesEvidence{
		Common: adapter.CommonEvidence{
			ExporterUp: true,
			ExporterText: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 87
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920`,
			Labels: adapter.CanonicalLabels{
				ClusterType: "kubernetes",
				Node:        "node-a",
			},
		},
		AllocationAvailable: true,
		PodResources: []adapter.PodResource{{
			Namespace: "default",
			Name:      "chat-a",
			Containers: []adapter.ContainerDevices{{
				ResourceName: "nvidia.com/gpu",
				DeviceIDs:    []string{"0"},
			}},
		}},
		EndpointPods: []adapter.EndpointPodEvidence{{
			Namespace: "default",
			Name:      "chat-a",
			NodeName:  "node-a",
			Labels: map[string]string{
				"endpoint":                         "chat",
				v1.NeutreeClusterWorkspaceLabelKey: "default",
			},
		}},
	})

	require.NoError(t, err)
	require.Len(t, result.Allocations, 1)
	assert.Equal(t, "chat", result.Allocations[0].Endpoint)
	require.Len(t, result.Allocations[0].Devices, 1)
	assert.Equal(t, "GPU-abc", result.Allocations[0].Devices[0].UUID)
	assert.Equal(t, int64(81920), result.Allocations[0].Devices[0].MemoryMiB)
	assert.Contains(t, sampleNames(result.Samples), "neutree_accelerator_utilization_ratio")
	assert.Contains(t, sampleNames(result.Samples), "neutree_endpoint_replica_accelerator_allocation")
}

func TestNvidiaAdapterBuildsHAMiAllocationsFromRawEvidence(t *testing.T) {
	accelerator := &nvidiaAccelerator{}
	result, err := accelerator.BuildKubernetesMetrics(context.Background(), testNvidiaHardware(), adapter.KubernetesEvidence{
		Common:              adapter.CommonEvidence{Labels: adapter.CanonicalLabels{Node: "node-a"}},
		AllocationAvailable: true,
		NodeLabels:          map[string]string{nvidiaGPUProductLabel: "Tesla-T4"},
		NodeAnnotations: map[string]string{
			nvidiaHAMiNodeNvidiaRegister: `[{"id":"GPU-abc","type":"NVIDIA-Tesla T4"}]`,
		},
		EndpointPods: []adapter.EndpointPodEvidence{{
			Namespace: "default",
			Name:      "chat-a",
			NodeName:  "node-a",
			Labels: map[string]string{
				"endpoint":                         "chat",
				v1.NeutreeClusterWorkspaceLabelKey: "default",
			},
			Annotations: map[string]string{
				nvidiaHAMiVGPUDevicesAllocated: ";GPU-abc,NVIDIA,4096,50:GPU-abc,NVIDIA,8192,50:;",
			},
		}},
	})

	require.NoError(t, err)
	require.Len(t, result.Allocations, 1)
	require.Len(t, result.Allocations[0].Devices, 2)
	assert.Equal(t, "GPU-abc", result.Allocations[0].Devices[0].UUID)
	assert.Equal(t, "0", result.Allocations[0].Devices[0].VDeviceIndex)
	assert.Equal(t, int64(4096), result.Allocations[0].Devices[0].MemoryMiB)
	assert.Equal(t, int64(50), result.Allocations[0].Devices[0].CoreUnits)
	assert.Equal(t, "Tesla-T4", result.Allocations[0].Devices[0].Product)
	assert.Equal(t, "1", result.Allocations[0].Devices[1].VDeviceIndex)
	assert.Equal(t, int64(8192), result.Allocations[0].Devices[1].MemoryMiB)
}

func TestNvidiaAdapterBuildsStaticAllocationFromRawRayEvidence(t *testing.T) {
	accelerator := &nvidiaAccelerator{}
	result, err := accelerator.BuildStaticMetrics(context.Background(), testNvidiaHardware(), adapter.StaticEvidence{
		Common: adapter.CommonEvidence{
			ExporterUp:   true,
			ExporterText: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 50`,
			Labels: adapter.CanonicalLabels{
				ClusterType: "ray",
				Node:        "head-0",
			},
		},
		AllocationAvailable: true,
		RayEvidence: adapter.RayEvidence{
			Actors: []adapter.RayActor{{ActorID: "actor-a", PID: 123, RequiredResources: map[string]float64{"GPU": 0.5}}},
			Replicas: []adapter.RayReplica{{
				Workspace: "default",
				Endpoint:  "chat",
				ActorID:   "actor-a",
				ReplicaID: "replica-a",
				NodeID:    "node-a",
			}},
			ActorProcesses: map[int]adapter.ProcessInfo{
				123: {PID: 123, Environment: map[string]string{"CUDA_VISIBLE_DEVICES": "0"}},
			},
		},
	})

	require.NoError(t, err)
	require.Len(t, result.Allocations, 1)
	assert.Equal(t, int64(40960), result.Allocations[0].Devices[0].MemoryMiB)
	assert.Equal(t, int64(50), result.Allocations[0].Devices[0].CoreUnits)
	assert.Equal(t, "head-0", result.Allocations[0].Devices[0].NodeID)
}

func TestNvidiaAdapterBuildsStaticSharedGPUAllocationsFromRawProcesses(t *testing.T) {
	firstMemoryUsedBytes := float64(3 * 1024 * 1024 * 1024)
	secondMemoryUsedBytes := float64(5 * 1024 * 1024 * 1024)
	accelerator := &nvidiaAccelerator{}
	result, err := accelerator.BuildStaticMetrics(context.Background(), testNvidiaHardware(), adapter.StaticEvidence{
		Common: adapter.CommonEvidence{
			ExporterUp: true,
			ExporterText: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 50
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="A100"} 8589934592`,
			Labels: adapter.CanonicalLabels{
				ClusterType: v1.SSHClusterType,
				Node:        "node-a",
			},
		},
		AllocationAvailable: true,
		RayEvidence: adapter.RayEvidence{
			Actors: []adapter.RayActor{
				{ActorID: "actor-a", PID: 100, RequiredResources: map[string]float64{"GPU": 0.4}},
				{ActorID: "actor-b", PID: 200, RequiredResources: map[string]float64{"GPU": 0.4}},
			},
			Replicas: []adapter.RayReplica{
				{Workspace: "default", Endpoint: "chat", ActorID: "actor-a", ReplicaID: "replica-a", NodeID: "node-a"},
				{Workspace: "default", Endpoint: "chat", ActorID: "actor-b", ReplicaID: "replica-b", NodeID: "node-a"},
			},
			ActorProcesses: map[int]adapter.ProcessInfo{
				100: {PID: 100, DescendantPIDs: []int{100, 101}, Environment: map[string]string{"NVIDIA_VISIBLE_DEVICES": "void"}},
				200: {PID: 200, DescendantPIDs: []int{200, 201}, Environment: map[string]string{"NVIDIA_VISIBLE_DEVICES": "void"}},
			},
			AcceleratorProcesses: []adapter.AcceleratorProcess{
				{DeviceID: "GPU-abc", PID: 101, MemoryUsedBytes: &firstMemoryUsedBytes},
				{DeviceID: "GPU-abc", PID: 201, MemoryUsedBytes: &secondMemoryUsedBytes},
			},
		},
	})

	require.NoError(t, err)
	require.Len(t, result.Allocations, 2)
	for _, allocation := range result.Allocations {
		require.Len(t, allocation.Devices, 1)
		assert.Equal(t, "GPU-abc", allocation.Devices[0].UUID)
		assert.Equal(t, int64(32768), allocation.Devices[0].MemoryMiB)
		assert.Equal(t, int64(40), allocation.Devices[0].CoreUnits)
	}

	memoryUsedByInstance := map[string]float64{}
	for _, sample := range result.Samples {
		if sample.Name == "neutree_endpoint_replica_accelerator_memory_used_bytes" {
			memoryUsedByInstance[sample.Labels["instance_id"]] = sample.Value
		}
	}

	assert.Equal(t, map[string]float64{
		"actor-a": firstMemoryUsedBytes,
		"actor-b": secondMemoryUsedBytes,
	}, memoryUsedByInstance)
	assert.Contains(t, sampleNames(result.Samples), "neutree_endpoint_replica_accelerator_allocation")
	assert.Contains(t, sampleNames(result.Samples), "neutree_endpoint_replica_accelerator_memory_allocated_bytes")
	assert.NotContains(t, sampleNames(result.Samples), "neutree_endpoint_replica_accelerator_utilization_ratio")
}

func TestNvidiaAdapterRetainsStaticExclusiveGPUUtilizationWithProcessMemory(t *testing.T) {
	memoryUsedBytes := float64(3 * 1024 * 1024 * 1024)
	accelerator := &nvidiaAccelerator{}
	result, err := accelerator.BuildStaticMetrics(context.Background(), testNvidiaHardware(), adapter.StaticEvidence{
		Common: adapter.CommonEvidence{
			ExporterUp: true,
			ExporterText: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 50
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="A100"} 8589934592`,
			Labels: adapter.CanonicalLabels{
				ClusterType: v1.SSHClusterType,
				Node:        "node-a",
			},
		},
		AllocationAvailable: true,
		RayEvidence: adapter.RayEvidence{
			Actors: []adapter.RayActor{{
				ActorID:           "actor-a",
				PID:               100,
				RequiredResources: map[string]float64{"GPU": 1},
			}},
			Replicas: []adapter.RayReplica{{
				Workspace: "default",
				Endpoint:  "chat",
				ActorID:   "actor-a",
				ReplicaID: "replica-a",
				NodeID:    "node-a",
			}},
			ActorProcesses: map[int]adapter.ProcessInfo{
				100: {PID: 100, DescendantPIDs: []int{100, 101}, Environment: map[string]string{"CUDA_VISIBLE_DEVICES": "0"}},
			},
			AcceleratorProcesses: []adapter.AcceleratorProcess{{
				DeviceID:        "GPU-abc",
				PID:             101,
				MemoryUsedBytes: &memoryUsedBytes,
			}},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, []float64{memoryUsedBytes}, sampleValues(
		result.Samples,
		"neutree_endpoint_replica_accelerator_memory_used_bytes",
	))
	assert.Equal(t, []float64{0.5}, sampleValues(
		result.Samples,
		"neutree_endpoint_replica_accelerator_utilization_ratio",
	))
}

func TestNvidiaStaticAllocationsKeepVisibleDevicesWhenProcessEvidenceIsPartial(t *testing.T) {
	hardwareSnapshot := adapter.HardwareSnapshot{Accelerator: v1.StaticNodeAcceleratorStatus{
		Type: v1.AcceleratorTypeNVIDIAGPU.String(),
		Devices: []v1.StaticNodeAcceleratorDeviceStatus{
			{ID: "0", UUID: "GPU-abc", ProductModel: "A100", MemoryMiB: 81920},
			{ID: "1", UUID: "GPU-def", ProductModel: "A100", MemoryMiB: 81920},
		},
	}}
	allocations := nvidiaStaticAllocations(hardwareSnapshot, adapter.StaticEvidence{
		Common:              adapter.CommonEvidence{Labels: adapter.CanonicalLabels{Node: "node-a"}},
		AllocationAvailable: true,
		RayEvidence: adapter.RayEvidence{
			Actors: []adapter.RayActor{{
				ActorID:           "actor-a",
				PID:               100,
				RequiredResources: map[string]float64{"GPU": 2},
			}},
			Replicas: []adapter.RayReplica{{
				Workspace: "default",
				Endpoint:  "chat",
				ActorID:   "actor-a",
				ReplicaID: "replica-a",
			}},
			ActorProcesses: map[int]adapter.ProcessInfo{
				100: {
					PID:            100,
					DescendantPIDs: []int{100, 101},
					Environment:    map[string]string{"CUDA_VISIBLE_DEVICES": "0,1"},
				},
			},
			AcceleratorProcesses: []adapter.AcceleratorProcess{{
				DeviceID: "GPU-abc",
				PID:      101,
			}},
		},
	})

	require.Len(t, allocations, 1)
	require.Len(t, allocations[0].Devices, 2)
	assert.Equal(t, []string{"GPU-abc", "GPU-def"}, []string{
		allocations[0].Devices[0].UUID,
		allocations[0].Devices[1].UUID,
	})
}

func TestNvidiaStaticAllocationsResolveVisibleDevicesConservatively(t *testing.T) {
	hardwareSnapshot := adapter.HardwareSnapshot{Accelerator: v1.StaticNodeAcceleratorStatus{
		Type: v1.AcceleratorTypeNVIDIAGPU.String(),
		Devices: []v1.StaticNodeAcceleratorDeviceStatus{
			{ID: "0", UUID: "GPU-abc", ProductModel: "A100", MemoryMiB: 81920},
			{ID: "1", UUID: "GPU-def", ProductModel: "A100", MemoryMiB: 81920},
		},
	}}
	testCases := []struct {
		name              string
		available         bool
		requiredResources map[string]float64
		deploymentOptions map[string]interface{}
		environment       map[string]string
		expectedUUID      string
		expectedCoreUnits int64
	}{
		{
			name:              "exact nvidia uuid wins over cuda index",
			available:         true,
			requiredResources: map[string]float64{"GPU": 0.5},
			environment: map[string]string{
				"NVIDIA_VISIBLE_DEVICES": "GPU-def",
				"CUDA_VISIBLE_DEVICES":   "0",
			},
			expectedUUID:      "GPU-def",
			expectedCoreUnits: 50,
		},
		{
			name:              "cuda index backs up ambiguous nvidia visibility",
			available:         true,
			requiredResources: map[string]float64{"gpu": 1},
			environment: map[string]string{
				"NVIDIA_VISIBLE_DEVICES": "all",
				"CUDA_VISIBLE_DEVICES":   "0",
			},
			expectedUUID:      "GPU-abc",
			expectedCoreUnits: 100,
		},
		{
			name:              "deployment options preserve fractional GPU when actor detail is absent",
			available:         true,
			deploymentOptions: map[string]interface{}{"num_gpus": 0.5},
			environment:       map[string]string{"CUDA_VISIBLE_DEVICES": "0"},
			expectedUUID:      "GPU-abc",
			expectedCoreUnits: 50,
		},
		{
			name:              "zero gpu resources are skipped",
			available:         true,
			requiredResources: map[string]float64{"GPU": 0},
			environment:       map[string]string{"CUDA_VISIBLE_DEVICES": "0"},
		},
		{
			name:              "unavailable evidence is not projected",
			available:         false,
			requiredResources: map[string]float64{"GPU": 1},
			environment:       map[string]string{"CUDA_VISIBLE_DEVICES": "0"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			allocations := nvidiaStaticAllocations(hardwareSnapshot, adapter.StaticEvidence{
				Common:              adapter.CommonEvidence{Labels: adapter.CanonicalLabels{Node: "head-0"}},
				AllocationAvailable: testCase.available,
				RayEvidence: adapter.RayEvidence{
					Actors: []adapter.RayActor{{
						ActorID:           "actor-a",
						PID:               123,
						RequiredResources: testCase.requiredResources,
					}},
					Replicas: []adapter.RayReplica{{
						Workspace:         "default",
						Endpoint:          "chat",
						ActorID:           "actor-a",
						ReplicaID:         "replica-a",
						DeploymentOptions: testCase.deploymentOptions,
					}},
					ActorProcesses: map[int]adapter.ProcessInfo{
						123: {PID: 123, Environment: testCase.environment},
					},
				},
			})

			if testCase.expectedUUID == "" {
				assert.Empty(t, allocations)
				return
			}

			require.Len(t, allocations, 1)
			require.Len(t, allocations[0].Devices, 1)
			assert.Equal(t, testCase.expectedUUID, allocations[0].Devices[0].UUID)
			assert.Equal(t, testCase.expectedCoreUnits, allocations[0].Devices[0].CoreUnits)
		})
	}
}

func TestNvidiaVisibleDeviceRefs(t *testing.T) {
	lookup := newNvidiaDeviceLookup([]v1.StaticNodeAcceleratorDeviceStatus{
		{ID: "0", UUID: "GPU-abc"},
		{ID: "1", UUID: "GPU-def"},
	})
	testCases := []struct {
		name        string
		environment map[string]string
		expected    []string
	}{
		{
			name:        "known nvidia uuid",
			environment: map[string]string{"NVIDIA_VISIBLE_DEVICES": "GPU-def"},
			expected:    []string{"GPU-def"},
		},
		{
			name: "unknown nvidia value falls back to cuda",
			environment: map[string]string{
				"NVIDIA_VISIBLE_DEVICES": "GPU-unknown",
				"CUDA_VISIBLE_DEVICES":   "1",
			},
			expected: []string{"1"},
		},
		{
			name:        "special values do not imply a device",
			environment: map[string]string{"NVIDIA_VISIBLE_DEVICES": "none"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, nvidiaVisibleDeviceRefs(testCase.environment, lookup))
		})
	}
}

func TestGPUHardwareInfosFromSnapshotRetainsImmutableDetails(t *testing.T) {
	assert.Nil(t, gpuHardwareInfosFromSnapshot(adapter.HardwareSnapshot{}))

	infos := gpuHardwareInfosFromSnapshot(adapter.HardwareSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{Devices: []v1.StaticNodeAcceleratorDeviceStatus{{
			ID:           "0",
			UUID:         "GPU-abc",
			ProductName:  "L20",
			MemoryMiB:    46068,
			ProductModel: "NVIDIA L20",
		}}},
		Details: []adapter.HardwareDetails{{
			UUID:              "GPU-abc",
			Architecture:      "Ada",
			CUDACapability:    "8.9",
			DriverVersion:     "550.54",
			CUDADriverVersion: "12.4",
			PCIEBusID:         "00000000:05:00.0",
			PCIEGeneration:    "4",
			PCIEWidth:         "16",
			NUMANode:          "0",
		}},
	})

	require.Len(t, infos, 1)
	assert.Equal(t, model.GPUHardwareInfo{
		UUID:              "GPU-abc",
		Index:             "0",
		Product:           "NVIDIA L20",
		Architecture:      "Ada",
		CUDACapability:    "8.9",
		DriverVersion:     "550.54",
		CUDADriverVersion: "12.4",
		MemoryTotalMiB:    "46068",
		PCIEBusID:         "00000000:05:00.0",
		PCIEGeneration:    "4",
		PCIEWidth:         "16",
		NUMANode:          "0",
	}, infos[0])
}

func TestNvidiaEndpointAllocationsFilterAndCloneDevices(t *testing.T) {
	order := 2
	allocations := []v1.StaticNodeAllocationStatus{
		{WorkloadType: "job", Endpoint: "ignored", Devices: []v1.DeviceAllocation{{UUID: "GPU-job"}}},
		{WorkloadType: "endpoint", Devices: []v1.DeviceAllocation{{UUID: "GPU-empty"}}},
		{
			WorkloadType: "endpoint",
			Endpoint:     "chat",
			InstanceID:   "actor-a",
			ReplicaID:    "replica-a",
			Devices: []v1.DeviceAllocation{{
				UUID:  "GPU-abc",
				Order: &order,
			}},
		},
	}

	endpointAllocations := nvidiaEndpointAllocations(adapter.CanonicalLabels{
		Workspace:      "fallback-workspace",
		NeutreeCluster: "cluster-a",
		NodeIP:         "10.0.0.10",
	}, allocations)

	require.Len(t, endpointAllocations, 1)
	assert.Equal(t, "fallback-workspace", endpointAllocations[0].Workspace)
	assert.Equal(t, "cluster-a", endpointAllocations[0].Cluster)
	assert.Equal(t, "10.0.0.10", endpointAllocations[0].NodeID)
	require.NotNil(t, endpointAllocations[0].Devices[0].Order)
	*endpointAllocations[0].Devices[0].Order = 9
	assert.Equal(t, 2, *allocations[2].Devices[0].Order)
}

func TestSortNvidiaAllocations(t *testing.T) {
	allocations := []v1.StaticNodeAllocationStatus{
		{Workspace: "b", Endpoint: "chat", InstanceID: "actor-a", RuntimeID: "runtime-a"},
		{Workspace: "a", Endpoint: "embed", InstanceID: "actor-a", RuntimeID: "runtime-a"},
		{Workspace: "a", Endpoint: "chat", InstanceID: "actor-b", RuntimeID: "runtime-a"},
		{Workspace: "a", Endpoint: "chat", InstanceID: "actor-a", RuntimeID: "runtime-z"},
		{Workspace: "a", Endpoint: "chat", InstanceID: "actor-a", RuntimeID: "runtime-a"},
	}

	sortAllocations(allocations)

	assert.Equal(t, []string{
		"a/chat/actor-a/runtime-a",
		"a/chat/actor-a/runtime-z",
		"a/chat/actor-b/runtime-a",
		"a/embed/actor-a/runtime-a",
		"b/chat/actor-a/runtime-a",
	}, []string{
		allocations[0].Workspace + "/" + allocations[0].Endpoint + "/" + allocations[0].InstanceID + "/" + allocations[0].RuntimeID,
		allocations[1].Workspace + "/" + allocations[1].Endpoint + "/" + allocations[1].InstanceID + "/" + allocations[1].RuntimeID,
		allocations[2].Workspace + "/" + allocations[2].Endpoint + "/" + allocations[2].InstanceID + "/" + allocations[2].RuntimeID,
		allocations[3].Workspace + "/" + allocations[3].Endpoint + "/" + allocations[3].InstanceID + "/" + allocations[3].RuntimeID,
		allocations[4].Workspace + "/" + allocations[4].Endpoint + "/" + allocations[4].InstanceID + "/" + allocations[4].RuntimeID,
	})
}

func testNvidiaHardware() adapter.HardwareSnapshot {
	return adapter.HardwareSnapshot{Accelerator: v1.StaticNodeAcceleratorStatus{
		Type: v1.AcceleratorTypeNVIDIAGPU.String(),
		Devices: []v1.StaticNodeAcceleratorDeviceStatus{{
			ID:           "0",
			UUID:         "GPU-abc",
			ProductName:  "A100",
			ProductModel: "A100",
			MemoryMiB:    81920,
			Healthy:      true,
		}},
	}}
}

func sampleNames(samples []adapter.Sample) map[string]struct{} {
	result := make(map[string]struct{}, len(samples))
	for _, sample := range samples {
		result[sample.Name] = struct{}{}
	}

	return result
}

func sampleValues(samples []adapter.Sample, name string) []float64 {
	result := make([]float64, 0)
	for _, sample := range samples {
		if sample.Name == name {
			result = append(result, sample.Value)
		}
	}

	return result
}
