package nodeagent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/normalizer"
)

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
				Workspace:   "default",
				Endpoint:    "chat",
				ActorID:     "actor-a",
				ReplicaID:   "replica-a",
				NodeID:      "node-a",
				GPUQuantity: 0.5,
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

func TestNvidiaAdapterPreservesLegacySamplesWithEndpointUsage(t *testing.T) {
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

	allocations := nvidiaKubernetesAllocations(hardware, evidence)
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
