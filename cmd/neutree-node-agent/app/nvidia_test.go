package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hardware"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
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
				UUID:           "GPU-def",
				Index:          "1",
				MinorNumber:    "3",
				Product:        "NVIDIA L20",
				Architecture:   "Ada",
				DriverVersion:  "550.54",
				MemoryTotalMiB: "46068",
				PCIEBusID:      "00000000:05:00.0",
				PCIEGeneration: "4",
				PCIEWidth:      "16",
				NUMANode:       "0",
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
			assert.Equal(t, "550.54", snapshot.Details[0].DriverVersion)
		})
	}
}

func TestNvidiaAdapterDeclaresNVIDIAInfoDescriptor(t *testing.T) {
	provider, ok := NewNVIDIAAdapter().(adapter.MetricDescriptorProvider)
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

func TestNvidiaAdapterEnrichesKubernetesEvidenceWithAdapterOwnedUsage(t *testing.T) {
	usedBytes := 1024.0
	accelerator := &nvidiaAccelerator{
		endpointUsageProvider: nvidiaEndpointUsageProviderFunc(func(context.Context) ([]adapter.EndpointReplicaAcceleratorUsage, error) {
			return []adapter.EndpointReplicaAcceleratorUsage{{
				Endpoint:        "chat",
				AcceleratorUUID: "GPU-abc",
				MemoryUsedBytes: &usedBytes,
			}}, nil
		}),
	}
	evidence := adapter.KubernetesEvidence{Common: adapter.CommonEvidence{
		Labels: adapter.CanonicalLabels{Node: "node-a"},
	}}

	enriched, err := accelerator.EnrichKubernetesEvidence(context.Background(), evidence)

	require.NoError(t, err)
	assert.Empty(t, evidence.Common.EndpointReplicaAcceleratorUsages)
	require.Len(t, enriched.Common.EndpointReplicaAcceleratorUsages, 1)
	assert.Equal(t, "GPU-abc", enriched.Common.EndpointReplicaAcceleratorUsages[0].AcceleratorUUID)
	require.NotNil(t, enriched.Common.EndpointReplicaAcceleratorUsages[0].MemoryUsedBytes)
	assert.Equal(t, 1024.0, *enriched.Common.EndpointReplicaAcceleratorUsages[0].MemoryUsedBytes)
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

func TestNvidiaStaticAllocationsUseAdapterProcessEvidenceWhenVisibilityIsUnset(t *testing.T) {
	allocations := nvidiaStaticAllocations(testNvidiaHardware(), adapter.StaticEvidence{
		Common:              adapter.CommonEvidence{Labels: adapter.CanonicalLabels{Node: "head-0"}},
		AllocationAvailable: true,
		RayEvidence: adapter.RayEvidence{
			Actors: []adapter.RayActor{{ActorID: "actor-a", PID: 123, RequiredResources: map[string]float64{"GPU": 1}}},
			Replicas: []adapter.RayReplica{{
				Workspace: "default",
				Endpoint:  "chat",
				ActorID:   "actor-a",
				ReplicaID: "replica-a",
			}},
			ActorProcesses: map[int]adapter.ProcessInfo{
				123: {PID: 123, DescendantPIDs: []int{123, 456}, Environment: map[string]string{"CUDA_VISIBLE_DEVICES": "all"}},
			},
			AcceleratorProcesses: []adapter.AcceleratorProcess{{DeviceID: "GPU-abc", PID: 456}},
		},
	})

	require.Len(t, allocations, 1)
	require.Len(t, allocations[0].Devices, 1)
	assert.Equal(t, "GPU-abc", allocations[0].Devices[0].UUID)
}

func TestNvidiaAdapterEnrichesStaticEvidenceWithAdapterProcessObservations(t *testing.T) {
	accelerator := &nvidiaAccelerator{
		processReader: nvidiaProcessReaderFunc(func(context.Context) ([]adapter.AcceleratorProcess, error) {
			return []adapter.AcceleratorProcess{{DeviceID: "GPU-abc", PID: 456}}, nil
		}),
	}
	evidence := adapter.StaticEvidence{RayEvidence: adapter.RayEvidence{
		ActorProcesses: map[int]adapter.ProcessInfo{123: {PID: 123}},
	}}

	enriched, err := accelerator.EnrichStaticEvidence(context.Background(), evidence)

	require.NoError(t, err)
	assert.Equal(t, []adapter.AcceleratorProcess{{DeviceID: "GPU-abc", PID: 456}}, enriched.RayEvidence.AcceleratorProcesses)
	assert.Empty(t, evidence.RayEvidence.AcceleratorProcesses)
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

func TestParseNvidiaSMIAcceleratorProcesses(t *testing.T) {
	processes := parseNvidiaSMIAcceleratorProcesses("GPU-def, 456\ninvalid\nGPU-abc, 123\nGPU-missing, no\n")

	assert.Equal(t, []adapter.AcceleratorProcess{
		{DeviceID: "GPU-abc", PID: 123},
		{DeviceID: "GPU-def", PID: 456},
	}, processes)
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
			UUID:           "GPU-abc",
			Architecture:   "Ada",
			DriverVersion:  "550.54",
			PCIEBusID:      "00000000:05:00.0",
			PCIEGeneration: "4",
			PCIEWidth:      "16",
			NUMANode:       "0",
		}},
	})

	require.Len(t, infos, 1)
	assert.Equal(t, model.GPUHardwareInfo{
		UUID:           "GPU-abc",
		Index:          "0",
		Product:        "NVIDIA L20",
		Architecture:   "Ada",
		DriverVersion:  "550.54",
		MemoryTotalMiB: "46068",
		PCIEBusID:      "00000000:05:00.0",
		PCIEGeneration: "4",
		PCIEWidth:      "16",
		NUMANode:       "0",
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

func TestNvidiaAdapterPreservesCompatibilitySamplesWithEndpointUsage(t *testing.T) {
	memoryUsedBytes := 4096.0 * 1024 * 1024
	utilizationRatio := 0.75
	evidence := adapter.KubernetesEvidence{
		Common: adapter.CommonEvidence{
			ExporterUp: true,
			ExporterText: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 87
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="A100"} 4096
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920`,
			Labels: adapter.CanonicalLabels{
				ClusterType: "kubernetes",
				Node:        "node-a",
			},
			EndpointReplicaAcceleratorUsages: []adapter.EndpointReplicaAcceleratorUsage{{
				Endpoint:         "chat",
				InstanceID:       "chat-a",
				ReplicaID:        "chat-a",
				NodeID:           "node-a",
				AcceleratorUUID:  "GPU-abc",
				AcceleratorIndex: "0",
				Product:          "A100",
				MemoryUsedBytes:  &memoryUsedBytes,
				UtilizationRatio: &utilizationRatio,
			}},
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
	}
	hardware := testNvidiaHardware()

	actual, err := (&nvidiaAccelerator{}).BuildKubernetesMetrics(context.Background(), hardware, evidence)
	require.NoError(t, err)

	allocations, err := nvidiaKubernetesAllocations(hardware, evidence)
	require.NoError(t, err)
	endpointAllocations := nvidiaEndpointAllocations(evidence.Common.Labels, allocations)
	endpointReplicaGPUUsages := internalEndpointReplicaAcceleratorUsages(
		evidence.Common.EndpointReplicaAcceleratorUsages,
	)
	labels := internalLabels(evidence.Common.Labels)
	hardwareInfos := gpuHardwareInfosFromSnapshot(hardware)
	acceleratorIndexes := normalizer.AcceleratorIndexesByUUID(evidence.Common.ExporterText, hardwareInfos)
	expected := make([]adapter.Sample, 0)
	expected = append(expected, adapterSamplesFromNormalizer(
		normalizer.NormalizeAcceleratorSamples(labels, evidence.Common.ExporterText),
	)...)
	expected = append(expected, adapterSamplesFromNormalizer(
		normalizer.NormalizeNodeGPUSamples(labels, evidence.Common.ExporterText, endpointAllocations),
	)...)
	expected = append(expected, adapterSamplesFromNormalizer(
		normalizer.NormalizeGPUHardwareInfoSamples(labels, hardwareInfos, evidence.Common.ExporterText),
	)...)
	expected = append(expected, adapterSamplesFromNormalizer(normalizer.NormalizeEndpointAllocationSamples(
		labels,
		endpointAllocations,
		endpointReplicaGPUUsages,
		acceleratorIndexes,
		evidence.Common.ExporterText,
	))...)
	expected = append(expected, adapterSamplesFromNormalizer(normalizer.NormalizeEndpointReplicaGPUUsageFromDCGMSamples(
		labels,
		evidence.Common.ExporterText,
		endpointAllocations,
		endpointReplicaGPUUsages,
	))...)
	expected = append(expected, adapterSamplesFromNormalizer(normalizer.NormalizeEndpointReplicaGPUUsageSamples(
		labels,
		endpointReplicaGPUUsages,
		endpointAllocations,
		acceleratorIndexes,
	))...)

	assert.Equal(t, expected, actual.Samples)
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

type nvidiaEndpointUsageProviderFunc func(context.Context) ([]adapter.EndpointReplicaAcceleratorUsage, error)

func (f nvidiaEndpointUsageProviderFunc) Usages(
	ctx context.Context,
) ([]adapter.EndpointReplicaAcceleratorUsage, error) {
	return f(ctx)
}
