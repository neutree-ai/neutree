package normalizer

import (
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/devicesnapshot"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/hardware"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func normalizeForTest(req NormalizeRequest) string {
	samples := (&Normalizer{}).Samples(req)

	var builder strings.Builder
	for _, sample := range samples {
		builder.WriteString(formatSample(sample))
		builder.WriteByte('\n')
	}

	return builder.String()
}

func TestNormalizerNormalizeNodeMetrics(t *testing.T) {
	output := normalizeForTest(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: model.ScrapeResult{
			Target: TargetNodeExporter,
			Up:     true,
			Body: `# HELP node_memory_MemTotal_bytes Memory information field MemTotal_bytes.
node_cpu_seconds_total{cpu="0",mode="idle"} 100
node_cpu_seconds_total{cpu="0",mode="user"} 20
node_memory_MemTotal_bytes 17179869184
node_memory_MemAvailable_bytes 6442450944
node_load1 2.5
`,
		},
	})

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",target="node-exporter"} 1`)
	assert.Contains(t, output, `neutree_node_ready{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent"} 1`)
	assert.Contains(t, output, `neutree_node_cpu_seconds_total{cluster_type="ray",cpu="0",mode="idle",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter"} 100`)
	assert.Contains(t, output, `neutree_node_cpu_seconds_total{cluster_type="ray",cpu="0",mode="user",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter"} 20`)
	assert.Contains(t, output, `neutree_node_memory_total_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter"} 17179869184`)
	assert.Contains(t, output, `neutree_node_memory_available_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter"} 6442450944`)
	assert.Contains(t, output, `neutree_node_memory_used_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter"} 10737418240`)
	assert.Contains(t, output, `neutree_node_load1{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter"} 2.5`)
}

func TestNormalizerLegacyPathEmitsAcceleratorSamples(t *testing.T) {
	// The legacy path (no --accelerator-type, AcceleratorSamples nil) keeps the
	// DCGM conversion behavior: it must still emit accelerator samples from a
	// DCGM exporter body.
	output := normalizeForTest(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: model.ScrapeResult{
			Target: TargetNodeExporter,
			Up:     false,
		},
		AcceleratorExporter: &model.ScrapeResult{
			Target: TargetAcceleratorExporter,
			Up:     true,
			Body: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 87
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 43008
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 81920
`,
		},
	})

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",target="accelerator-exporter"} 1`)
	assert.Contains(t, output, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 0.87`)
	assert.Contains(t, output, `neutree_accelerator_memory_used_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 45097156608`)
	assert.Contains(t, output, `neutree_node_accelerator_total{accelerator_type="nvidia_gpu",cluster_type="ray",node="head-0",product="A100"} 1`)
	assert.NotContains(t, output, "neutree_metrics_mapping_supported")
}

func TestNormalizerUsesPrecomputedAcceleratorSamples(t *testing.T) {
	// The adapter path (--accelerator-type set) passes pre-computed accelerator
	// samples through the normalizer's generic sample exit. The normalizer emits
	// them unchanged alongside scrape-up samples.
	precomputed := []Sample{
		{
			Name: "neutree_accelerator_utilization_ratio",
			Labels: map[string]string{
				"cluster_type":      "kubernetes",
				"node":              "node-a",
				"accelerator_type":  "npu",
				"accelerator_uuid":  "vdie-1",
				"accelerator_index": "0",
				"product":           "310P3-Ascend-V1",
			},
			Value: 0.5,
		},
	}

	output := normalizeForTest(NormalizeRequest{
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		NodeExporter: model.ScrapeResult{
			Target: TargetNodeExporter,
			Up:     false,
		},
		AcceleratorExporter: &model.ScrapeResult{
			Target: TargetAcceleratorExporter,
			Up:     true,
			Body:   "not-a-dcgm-body",
		},
		AcceleratorSamples: precomputed,
	})

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",source="neutree-node-agent",target="accelerator-exporter"} 1`)
	assert.Contains(t, output, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="npu",accelerator_uuid="vdie-1",cluster_type="kubernetes",node="node-a",product="310P3-Ascend-V1"} 0.5`)
	// The pre-computed samples replace the legacy DCGM conversion: a non-DCGM
	// exporter body must not produce DCGM-derived samples.
	assert.NotContains(t, output, `neutree_node_accelerator_total{`)
}

func TestNormalizerNormalizesEndpointReplicaRuntimeUsage(t *testing.T) {
	usageBytes := 1024.0
	workingSetBytes := 768.0
	cpuLimitCores := 2.5
	memoryLimitBytes := 2048.0

	output := normalizeForTest(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: model.ScrapeResult{
			Target: TargetNodeExporter,
			Up:     false,
		},
		EndpointReplicaRuntimeUsages: []model.EndpointReplicaRuntimeUsage{
			{
				Workspace:             "default",
				Cluster:               "static-a",
				Endpoint:              "chat",
				InstanceID:            "actor-a",
				ReplicaID:             "replica-a",
				NodeID:                "head-0",
				WorkloadRole:          model.WorkloadRoleBackend,
				Container:             "engine",
				ContainerID:           "docker-abc",
				Engine:                "vllm",
				EngineVersion:         "v0.17.1",
				CPUUsageSeconds:       12.5,
				MemoryUsageBytes:      &usageBytes,
				MemoryWorkingSetBytes: &workingSetBytes,
				CPULimitCores:         &cpuLimitCores,
				MemoryLimitBytes:      &memoryLimitBytes,
			},
		},
	})

	commonLabels := `cluster_type="ray",container="engine",container_id="docker-abc",` +
		`endpoint="chat",engine="vllm",engine_version="v0.17.1",instance_id="actor-a",` +
		`node="head-0",node_ip="10.0.0.10",node_role="head",` +
		`replica="replica-a",source="neutree-node-agent",workload_role="backend"`
	assert.Contains(t, output, `neutree_endpoint_replica_cpu_usage_seconds_total{`+commonLabels+`} 12.5`)
	assert.Contains(t, output, `neutree_endpoint_replica_memory_usage_bytes{`+commonLabels+`} 1024`)
	assert.Contains(t, output, `neutree_endpoint_replica_memory_working_set_bytes{`+commonLabels+`} 768`)
	assert.Contains(t, output, `neutree_endpoint_replica_cpu_limit_cores{`+commonLabels+`} 2.5`)
	assert.Contains(t, output, `neutree_endpoint_replica_memory_limit_bytes{`+commonLabels+`} 2048`)
}

func TestNormalizerNormalizesEndpointReplicaGPURuntimeUsage(t *testing.T) {
	usedBytes := 4096.0 * 1024 * 1024
	utilization := 0.75

	output := normalizeForTest(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: model.ScrapeResult{
			Target: TargetNodeExporter,
			Up:     false,
		},
		EndpointReplicaGPUUsages: []model.EndpointReplicaGPUUsage{
			{
				Workspace:        "default",
				Cluster:          "static-a",
				Endpoint:         "chat",
				InstanceID:       "chat-abc",
				ReplicaID:        "chat-abc",
				NodeID:           "head-0",
				Container:        "engine",
				GPUUUID:          "GPU-abc",
				AcceleratorIndex: "0",
				VDeviceIndex:     "2",
				Product:          "NVIDIA_A100",
				MemoryUsedBytes:  &usedBytes,
				UtilizationRatio: &utilization,
			},
		},
	})

	commonLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="ray",endpoint="chat",instance_id="chat-abc",` +
		`node="head-0",product="NVIDIA_A100",replica="chat-abc",vdevice_index="2"`
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+commonLabels+`} 4294967296`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_utilization_ratio{`+commonLabels+`} 0.75`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_allocation{`+commonLabels+`}`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_memory_allocated_bytes{`+commonLabels+`}`)
	assert.NotContains(t, output, "container=")
	assert.NotContains(t, output, "gpu_uuid=")
}

func TestNormalizerKeepsRepeatedGPUAllocationsDistinctByVDeviceIndex(t *testing.T) {
	output := normalizeForTest(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: model.ScrapeResult{
			Target: TargetNodeExporter,
			Up:     false,
		},
		EndpointAllocations: []model.EndpointAllocation{
			{
				Workspace:  "default",
				Cluster:    "static-a",
				Endpoint:   "chat",
				InstanceID: "chat-abc",
				ReplicaID:  "chat-abc",
				NodeID:     "head-0",
				Devices: []v1.DeviceAllocation{
					{
						UUID:         "GPU-abc",
						Product:      "NVIDIA_A100",
						VDeviceIndex: "0",
						MemoryMiB:    4096,
						NodeID:       "head-0",
					},
					{
						UUID:         "GPU-abc",
						Product:      "NVIDIA_A100",
						VDeviceIndex: "1",
						MemoryMiB:    8192,
						NodeID:       "head-0",
					},
				},
			},
		},
	})

	firstLabels := `accelerator_index="unknown",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="ray",endpoint="chat",instance_id="chat-abc",` +
		`node="head-0",product="NVIDIA_A100",replica="chat-abc",vdevice_index="0"`
	secondLabels := `accelerator_index="unknown",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="ray",endpoint="chat",instance_id="chat-abc",` +
		`node="head-0",product="NVIDIA_A100",replica="chat-abc",vdevice_index="1"`
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_allocated_bytes{`+firstLabels+`} 4294967296`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_allocated_bytes{`+secondLabels+`} 8589934592`)
	assert.Equal(t, 2, strings.Count(output, "neutree_endpoint_replica_accelerator_memory_allocated_bytes"))
}

func TestNormalizerMatchesExplicitGPUUsageToAllocationWithNodeFallback(t *testing.T) {
	usedBytes := 4096.0 * 1024 * 1024

	output := normalizeForTest(NormalizeRequest{
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		NodeExporter: model.ScrapeResult{
			Target: TargetNodeExporter,
			Up:     false,
		},
		EndpointAllocations: []model.EndpointAllocation{
			{
				Workspace:  "default",
				Cluster:    "k8s-a",
				Endpoint:   "chat",
				InstanceID: "chat-abc",
				ReplicaID:  "chat-abc",
				Devices: []v1.DeviceAllocation{
					{UUID: "GPU-abc", Product: "Tesla-T4", MemoryMiB: 8192, CoreUnits: 50},
				},
			},
		},
		EndpointReplicaGPUUsages: []model.EndpointReplicaGPUUsage{
			{
				Endpoint:         "chat",
				InstanceID:       "chat-abc",
				ReplicaID:        "chat-abc",
				GPUUUID:          "GPU-abc",
				AcceleratorIndex: "0",
				VDeviceIndex:     "0",
				Product:          "Tesla-T4",
				MemoryUsedBytes:  &usedBytes,
			},
		},
	})

	allocationInfoLabels := `accelerator_index="unknown",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="kubernetes",endpoint="chat",instance_id="chat-abc",` +
		`node="node-a",physical_vram_usage="unknown",product="Tesla-T4",replica="chat-abc",vdevice_index="0",vram_usage="4 GiB / 8 GiB"`
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_allocation{`+allocationInfoLabels+`} 1`)
}

func TestNormalizerDerivesAllocationVRAMFromUniqueExplicitGPUUsageWhenVDeviceIndexDiffers(t *testing.T) {
	usedBytes := 28038.0 * 1024 * 1024

	output := normalizeForTest(NormalizeRequest{
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		NodeExporter: model.ScrapeResult{
			Target: TargetNodeExporter,
			Up:     false,
		},
		EndpointAllocations: []model.EndpointAllocation{
			{
				Workspace:  "default",
				Cluster:    "k8s-a",
				Endpoint:   "chat",
				InstanceID: "chat-abc",
				ReplicaID:  "chat-abc",
				Devices: []v1.DeviceAllocation{
					{UUID: "GPU-def", Product: "NVIDIA-L20", VDeviceIndex: "0", MemoryMiB: 30720, CoreUnits: 50, NodeID: "node-a"},
				},
			},
		},
		EndpointReplicaGPUUsages: []model.EndpointReplicaGPUUsage{
			{
				Endpoint:        "chat",
				InstanceID:      "chat-abc",
				ReplicaID:       "chat-abc",
				NodeID:          "node-a",
				GPUUUID:         "GPU-def",
				VDeviceIndex:    "1",
				MemoryUsedBytes: &usedBytes,
			},
		},
	})

	allocationInfoLabels := `accelerator_index="unknown",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-def",` +
		`cluster_type="kubernetes",endpoint="chat",instance_id="chat-abc",` +
		`node="node-a",physical_vram_usage="unknown",product="NVIDIA-L20",replica="chat-abc",vdevice_index="0",vram_usage="27.4 GiB / 30 GiB"`
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_allocation{`+allocationInfoLabels+`} 1`)
}

func TestGPUHardwareInfosFromAcceleratorMetrics(t *testing.T) {
	raw := `DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920
DCGM_FI_DRIVER_VERSION{gpu="0",UUID="GPU-abc",modelName="A100",Driver_Version="535.104.05"} 1
DCGM_FI_CUDA_DRIVER_VERSION{gpu="0",UUID="GPU-abc",modelName="A100"} 12020
DCGM_FI_DEV_CUDA_COMPUTE_CAPABILITY{gpu="0",UUID="GPU-abc",modelName="A100",cuda_compute_capability="8.0"} 0
DCGM_FI_DEV_PCI_BUSID{gpu="0",UUID="GPU-abc",modelName="A100",pci_bus_id="00000000:3B:00.0"} 1
DCGM_FI_DEV_PCIE_LINK_GEN{gpu="0",UUID="GPU-abc",modelName="A100"} 4
DCGM_FI_DEV_PCIE_LINK_WIDTH{gpu="0",UUID="GPU-abc",modelName="A100"} 16
DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 1
DCGM_FI_DEV_NVSWITCH_LINK_STATUS{nvswitch="0",link="0"} 2
`

	infos := hardware.FromAcceleratorMetrics(raw)

	require.Len(t, infos, 1)
	assert.Equal(t, "GPU-abc", infos[0].UUID)
	assert.Equal(t, "0", infos[0].Index)
	assert.Equal(t, "A100", infos[0].Product)
	assert.Equal(t, "535.104.05", infos[0].DriverVersion)
	assert.Equal(t, "12.2", infos[0].CUDADriverVersion)
	assert.Equal(t, "8.0", infos[0].CUDACapability)
	assert.Equal(t, "81920", infos[0].MemoryTotalMiB)
	assert.Equal(t, "00000000:3B:00.0", infos[0].PCIEBusID)
	assert.Equal(t, "4", infos[0].PCIEGeneration)
	assert.Equal(t, "16", infos[0].PCIEWidth)
	assert.Equal(t, "1", infos[0].NVLink)
	assert.Equal(t, "1", infos[0].NVSwitch)
}

func TestNormalizerParsesDCGMLabelsWithSpaces(t *testing.T) {
	raw := `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="Tesla T4",Hostname="gpu-node"} 87
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="Tesla T4",Hostname="gpu-node"} 15360
`

	output := normalizeForTest(NormalizeRequest{
		Labels: testLabels(),
		AcceleratorExporter: &model.ScrapeResult{
			Target: TargetAcceleratorExporter,
			Up:     true,
			Body:   raw,
		},
	})

	assert.Contains(t, output, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="Tesla T4"} 0.87`)
	assert.Contains(t, output, `neutree_accelerator_memory_total_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="Tesla T4"} 16106127360`)

	snapshot := devicesnapshot.FromAcceleratorMetrics(raw)
	require.NotNil(t, snapshot)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), snapshot.Accelerator.Type)
	require.Len(t, snapshot.Accelerator.Devices, 1)
	assert.Equal(t, "GPU-abc", snapshot.Accelerator.Devices[0].UUID)
	assert.Equal(t, "Tesla T4", snapshot.Accelerator.Devices[0].ProductName)
	assert.Equal(t, int64(15360), snapshot.Accelerator.Devices[0].MemoryMiB)
}

func testLabels() model.CanonicalLabels {
	return model.CanonicalLabels{
		Workspace:         "default",
		StaticNodeCluster: "static-a",
		ClusterType:       "ray",
		Node:              "head-0",
		NodeIP:            "10.0.0.10",
		NodeRole:          "head",
	}
}
