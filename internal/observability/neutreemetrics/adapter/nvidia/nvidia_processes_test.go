package nvidia

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestNvidiaAdapterBuildsStaticAllocationAndMemoryFromDescendantGPUProcess(t *testing.T) {
	accelerator := &accelerator{
		processReader: nvidiaGPUProcessReaderFunc(func(context.Context) ([]nvidiaGPUProcess, error) {
			return []nvidiaGPUProcess{{UUID: "GPU-abc", PID: 456, UsedMemoryMiB: 4096}}, nil
		}),
	}

	result, err := accelerator.BuildStaticMetrics(context.Background(), testNvidiaHardware(), adapter.StaticEvidence{
		Common: adapter.CommonEvidence{
			ExporterUp: true,
			ExporterText: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 50
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920`,
			Labels: adapter.CanonicalLabels{
				ClusterType: v1.SSHClusterType,
				Node:        "head-0",
			},
		},
		AllocationAvailable: true,
		RayEvidence: adapter.RayEvidence{
			Actors: []adapter.RayActor{{
				ActorID:           "actor-a",
				PID:               123,
				RequiredResources: map[string]float64{"GPU": 0.5},
			}},
			Replicas: []adapter.RayReplica{{
				Workspace: "default",
				Endpoint:  "chat",
				ActorID:   "actor-a",
				ReplicaID: "replica-a",
			}},
			ActorProcesses: map[int]adapter.ProcessInfo{
				123: {
					PID:            123,
					DescendantPIDs: []int{123, 456},
					Environment:    map[string]string{"NVIDIA_VISIBLE_DEVICES": "void"},
				},
			},
		},
	})

	require.NoError(t, err)
	require.Len(t, result.Allocations, 1)
	require.Len(t, result.Allocations[0].Devices, 1)
	device := result.Allocations[0].Devices[0]
	assert.Equal(t, "GPU-abc", device.UUID)
	assert.Equal(t, int64(40960), device.MemoryMiB)
	assert.Equal(t, int64(50), device.CoreUnits)
	assert.Equal(t, int64(4096), device.UsedMemoryMiB)

	memorySamples := samplesNamed(result.Samples, "neutree_endpoint_replica_accelerator_memory_used_bytes")
	require.Len(t, memorySamples, 1)
	assert.Equal(t, 4096.0*1024*1024, memorySamples[0].Value)
}

func TestNvidiaAdapterDoesNotDuplicatePhysicalUtilizationForSharedStaticGPU(t *testing.T) {
	accelerator := &accelerator{
		processReader: nvidiaGPUProcessReaderFunc(func(context.Context) ([]nvidiaGPUProcess, error) {
			return []nvidiaGPUProcess{
				{UUID: "GPU-abc", PID: 456, UsedMemoryMiB: 1024},
				{UUID: "GPU-abc", PID: 789, UsedMemoryMiB: 2048},
			}, nil
		}),
	}

	result, err := accelerator.BuildStaticMetrics(context.Background(), testNvidiaHardware(), adapter.StaticEvidence{
		Common: adapter.CommonEvidence{
			ExporterUp: true,
			ExporterText: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 50
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920`,
			Labels: adapter.CanonicalLabels{
				ClusterType: v1.SSHClusterType,
				Node:        "head-0",
			},
		},
		AllocationAvailable: true,
		RayEvidence: adapter.RayEvidence{
			Actors: []adapter.RayActor{
				{ActorID: "actor-a", PID: 123, RequiredResources: map[string]float64{"GPU": 0.4}},
				{ActorID: "actor-b", PID: 124, RequiredResources: map[string]float64{"GPU": 0.4}},
			},
			Replicas: []adapter.RayReplica{
				{Workspace: "default", Endpoint: "chat", ActorID: "actor-a", ReplicaID: "replica-a"},
				{Workspace: "default", Endpoint: "chat", ActorID: "actor-b", ReplicaID: "replica-b"},
			},
			ActorProcesses: map[int]adapter.ProcessInfo{
				123: {PID: 123, DescendantPIDs: []int{123, 456}, Environment: map[string]string{"NVIDIA_VISIBLE_DEVICES": "void"}},
				124: {PID: 124, DescendantPIDs: []int{124, 789}, Environment: map[string]string{"NVIDIA_VISIBLE_DEVICES": "void"}},
			},
		},
	})

	require.NoError(t, err)
	require.Len(t, result.Allocations, 2)
	assert.Len(t, samplesNamed(result.Samples, "neutree_endpoint_replica_accelerator_memory_used_bytes"), 2)
	assert.NotContains(t, sampleNames(result.Samples), "neutree_endpoint_replica_accelerator_utilization_ratio")
}

func TestParseNvidiaSMIComputeProcesses(t *testing.T) {
	processes := parseNvidiaSMIComputeProcesses(`
GPU-def, 456, 512 MiB
invalid
GPU-abc, 123, 4096
GPU-missing, no, 100
`)

	assert.Equal(t, []nvidiaGPUProcess{
		{UUID: "GPU-abc", PID: 123, UsedMemoryMiB: 4096},
		{UUID: "GPU-def", PID: 456, UsedMemoryMiB: 512},
	}, processes)
}

func samplesNamed(samples []adapter.Sample, name string) []adapter.Sample {
	result := make([]adapter.Sample, 0)
	for _, sample := range samples {
		if sample.Name == name {
			result = append(result, sample)
		}
	}

	return result
}
