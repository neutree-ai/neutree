package nvidia

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestNvidiaBuildMetricSamplesUsesAdapterEndpointUsage(t *testing.T) {
	memoryUsedBytes := 1024.0
	utilizationRatio := 0.75

	samples := nvidiaBuildMetricSamples(
		adapter.CanonicalLabels{ClusterType: "kubernetes", Node: "node-a"},
		"",
		nil,
		nil,
		[]adapter.EndpointReplicaAcceleratorUsage{{
			Endpoint:         "chat",
			InstanceID:       "replica-a",
			ReplicaID:        "replica-a",
			NodeID:           "node-a",
			AcceleratorUUID:  "GPU-a",
			MemoryUsedBytes:  &memoryUsedBytes,
			UtilizationRatio: &utilizationRatio,
		}},
	)

	assert.Contains(t, samples, adapter.Sample{
		Name: "neutree_endpoint_replica_accelerator_memory_used_bytes",
		Labels: map[string]string{
			"cluster_type":      "kubernetes",
			"endpoint":          "chat",
			"instance_id":       "replica-a",
			"replica":           "replica-a",
			"node":              "node-a",
			"accelerator_type":  "nvidia_gpu",
			"accelerator_uuid":  "GPU-a",
			"accelerator_index": "unknown",
			"vdevice_index":     "0",
			"product":           "unknown",
		},
		Value: memoryUsedBytes,
	})
	assert.Contains(t, samples, adapter.Sample{
		Name: "neutree_endpoint_replica_accelerator_utilization_ratio",
		Labels: map[string]string{
			"cluster_type":      "kubernetes",
			"endpoint":          "chat",
			"instance_id":       "replica-a",
			"replica":           "replica-a",
			"node":              "node-a",
			"accelerator_type":  "nvidia_gpu",
			"accelerator_uuid":  "GPU-a",
			"accelerator_index": "unknown",
			"vdevice_index":     "0",
			"product":           "unknown",
		},
		Value: utilizationRatio,
	})
}

func TestNvidiaBuildMetricSamplesKeepsRepeatedAllocationsDistinctByVDeviceIndex(t *testing.T) {
	labels := adapter.CanonicalLabels{ClusterType: "kubernetes", Node: "node-a"}
	samples := nvidiaBuildMetricSamples(
		labels,
		nvidiaDCGMFixture("GPU-a", "A100", "0", 62, 2048, 81920),
		nil,
		[]model.EndpointAllocation{{
			Endpoint: "chat", InstanceID: "replica-a", ReplicaID: "replica-a", NodeID: "node-a",
			Devices: []v1.DeviceAllocation{
				{UUID: "GPU-a", Product: "A100", VDeviceIndex: "0", MemoryMiB: 4096, NodeID: "node-a"},
				{UUID: "GPU-a", Product: "A100", VDeviceIndex: "1", MemoryMiB: 8192, NodeID: "node-a"},
			},
		}},
		nil,
	)

	first := requireNvidiaSample(t, samples, "neutree_endpoint_replica_accelerator_memory_allocated_bytes", map[string]string{
		"endpoint": "chat", "accelerator_uuid": "GPU-a", "vdevice_index": "0",
	})
	second := requireNvidiaSample(t, samples, "neutree_endpoint_replica_accelerator_memory_allocated_bytes", map[string]string{
		"endpoint": "chat", "accelerator_uuid": "GPU-a", "vdevice_index": "1",
	})

	assert.Equal(t, float64(4096*1024*1024), first.Value)
	assert.Equal(t, float64(8192*1024*1024), second.Value)
	assert.Len(t, nvidiaSamplesNamed(samples, "neutree_endpoint_replica_accelerator_memory_allocated_bytes"), 2)
}

func TestNvidiaBuildMetricSamplesDerivesUsageOnlyForUniqueAllocation(t *testing.T) {
	labels := adapter.CanonicalLabels{ClusterType: "kubernetes", Node: "node-a"}
	raw := nvidiaDCGMFixture("GPU-a", "A100", "0", 62, 2048, 81920)
	uniqueSamples := nvidiaBuildMetricSamples(
		labels,
		raw,
		nil,
		[]model.EndpointAllocation{nvidiaTestAllocation("chat-a", "replica-a", "GPU-a", "A100", 81920)},
		nil,
	)

	used := requireNvidiaSample(t, uniqueSamples, "neutree_endpoint_replica_accelerator_memory_used_bytes", map[string]string{
		"endpoint": "chat-a", "accelerator_uuid": "GPU-a", "accelerator_index": "0",
	})
	utilization := requireNvidiaSample(t, uniqueSamples, "neutree_endpoint_replica_accelerator_utilization_ratio", map[string]string{
		"endpoint": "chat-a", "accelerator_uuid": "GPU-a", "accelerator_index": "0",
	})
	allocation := requireNvidiaSample(t, uniqueSamples, "neutree_endpoint_replica_accelerator_allocation", map[string]string{
		"endpoint": "chat-a", "accelerator_uuid": "GPU-a",
	})
	assert.Equal(t, float64(2048*1024*1024), used.Value)
	assert.Equal(t, 0.62, utilization.Value)
	assert.Equal(t, "2 GiB / 80 GiB", allocation.Labels["vram_usage"])

	sharedSamples := nvidiaBuildMetricSamples(
		labels,
		raw,
		nil,
		[]model.EndpointAllocation{
			nvidiaTestAllocation("chat-a", "replica-a", "GPU-a", "A100", 40960),
			nvidiaTestAllocation("chat-b", "replica-b", "GPU-a", "A100", 40960),
		},
		nil,
	)

	assert.Empty(t, nvidiaSamplesNamed(sharedSamples, "neutree_endpoint_replica_accelerator_memory_used_bytes"))
	assert.Empty(t, nvidiaSamplesNamed(sharedSamples, "neutree_endpoint_replica_accelerator_utilization_ratio"))
}

func TestNvidiaBuildMetricSamplesUsesExplicitUsageForSharedAllocation(t *testing.T) {
	labels := adapter.CanonicalLabels{ClusterType: "kubernetes", Node: "node-a"}
	chatAUsedBytes := float64(4096 * 1024 * 1024)
	chatBUsedBytes := float64(3072 * 1024 * 1024)
	chatAUtilization := 0.25
	chatBUtilization := 0.75
	samples := nvidiaBuildMetricSamples(
		labels,
		nvidiaDCGMFixture("GPU-a", "Tesla-T4", "0", 62, 12288, 15360),
		nil,
		[]model.EndpointAllocation{
			nvidiaTestAllocation("chat-a", "replica-a", "GPU-a", "Tesla-T4", 8192),
			nvidiaTestAllocation("chat-b", "replica-b", "GPU-a", "Tesla-T4", 7168),
		},
		[]adapter.EndpointReplicaAcceleratorUsage{
			{Endpoint: "chat-a", InstanceID: "replica-a", ReplicaID: "replica-a", AcceleratorUUID: "GPU-a", VDeviceIndex: "0", MemoryUsedBytes: &chatAUsedBytes, UtilizationRatio: &chatAUtilization},
			{Endpoint: "chat-b", InstanceID: "replica-b", ReplicaID: "replica-b", AcceleratorUUID: "GPU-a", VDeviceIndex: "0", MemoryUsedBytes: &chatBUsedBytes, UtilizationRatio: &chatBUtilization},
		},
	)

	chatAUsage := requireNvidiaSample(t, samples, "neutree_endpoint_replica_accelerator_memory_used_bytes", map[string]string{
		"endpoint": "chat-a", "accelerator_uuid": "GPU-a", "accelerator_index": "0", "product": "Tesla-T4",
	})
	chatBUsage := requireNvidiaSample(t, samples, "neutree_endpoint_replica_accelerator_memory_used_bytes", map[string]string{
		"endpoint": "chat-b", "accelerator_uuid": "GPU-a", "accelerator_index": "0", "product": "Tesla-T4",
	})
	chatAAllocation := requireNvidiaSample(t, samples, "neutree_endpoint_replica_accelerator_allocation", map[string]string{
		"endpoint": "chat-a", "accelerator_uuid": "GPU-a",
	})
	chatBAllocation := requireNvidiaSample(t, samples, "neutree_endpoint_replica_accelerator_allocation", map[string]string{
		"endpoint": "chat-b", "accelerator_uuid": "GPU-a",
	})

	assert.Equal(t, chatAUsedBytes, chatAUsage.Value)
	assert.Equal(t, chatBUsedBytes, chatBUsage.Value)
	assert.Equal(t, "4 GiB / 8 GiB", chatAAllocation.Labels["vram_usage"])
	assert.Equal(t, "3 GiB / 7 GiB", chatBAllocation.Labels["vram_usage"])
}

func TestNvidiaBuildMetricSamplesDoesNotMutateExplicitUsage(t *testing.T) {
	memoryUsedBytes := float64(4096 * 1024 * 1024)
	usage := adapter.EndpointReplicaAcceleratorUsage{
		Endpoint: "chat", InstanceID: "replica-a", ReplicaID: "replica-a", AcceleratorUUID: "GPU-a", VDeviceIndex: "1", MemoryUsedBytes: &memoryUsedBytes,
	}

	nvidiaBuildMetricSamples(
		adapter.CanonicalLabels{ClusterType: "kubernetes", Node: "node-a"},
		nvidiaDCGMFixture("GPU-a", "Tesla-T4", "0", 62, 4096, 15360),
		nil,
		[]model.EndpointAllocation{nvidiaTestAllocation("chat", "replica-a", "GPU-a", "Tesla-T4", 8192)},
		[]adapter.EndpointReplicaAcceleratorUsage{usage},
	)

	assert.Equal(t, "", usage.NodeID)
	assert.Equal(t, "", usage.Product)
	assert.Equal(t, "", usage.AcceleratorIndex)
	assert.Equal(t, "1", usage.VDeviceIndex)
	assert.Same(t, &memoryUsedBytes, usage.MemoryUsedBytes)
}

func nvidiaTestAllocation(endpoint, replicaID, uuid, product string, memoryMiB int64) model.EndpointAllocation {
	return model.EndpointAllocation{
		Endpoint: endpoint, InstanceID: replicaID, ReplicaID: replicaID, NodeID: "node-a",
		Devices: []v1.DeviceAllocation{{
			UUID: uuid, Product: product, MemoryMiB: memoryMiB, NodeID: "node-a",
		}},
	}
}

func nvidiaDCGMFixture(uuid, product, index string, utilization, usedMiB, totalMiB float64) string {
	return "DCGM_FI_DEV_GPU_UTIL{gpu=\"" + index + "\",UUID=\"" + uuid + "\",modelName=\"" + product + "\"} " +
		nvidiaFormatFloat(utilization) + "\n" +
		"DCGM_FI_DEV_FB_USED{gpu=\"" + index + "\",UUID=\"" + uuid + "\",modelName=\"" + product + "\"} " +
		nvidiaFormatFloat(usedMiB) + "\n" +
		"DCGM_FI_DEV_FB_TOTAL{gpu=\"" + index + "\",UUID=\"" + uuid + "\",modelName=\"" + product + "\"} " +
		nvidiaFormatFloat(totalMiB) + "\n"
}

func requireNvidiaSample(t *testing.T, samples []adapter.Sample, name string, labels map[string]string) adapter.Sample {
	t.Helper()

	for _, sample := range samples {
		if sample.Name != name {
			continue
		}

		matches := true
		for key, expected := range labels {
			if sample.Labels[key] != expected {
				matches = false
				break
			}
		}
		if matches {
			return sample
		}
	}

	require.Failf(t, "expected NVIDIA sample", "%s with labels %v was not present", name, labels)

	return adapter.Sample{}
}

func nvidiaSamplesNamed(samples []adapter.Sample, name string) []adapter.Sample {
	result := make([]adapter.Sample, 0)
	for _, sample := range samples {
		if sample.Name == name {
			result = append(result, sample)
		}
	}

	return result
}
