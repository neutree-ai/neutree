package normalizer

import (
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/devicesnapshot"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hardware"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
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

func TestNormalizerNormalizesAcceleratorExporterAndEndpointAllocations(t *testing.T) {
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
	DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 72
	DCGM_FI_PROF_PCIE_TX_BYTES{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 1024
	DCGM_FI_PROF_PCIE_RX_BYTES{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 2048
	DCGM_FI_DEV_GPU_UTIL{gpu="1",UUID="GPU-def",device="nvidia1",modelName="A100"} 0
DCGM_FI_DEV_FB_USED{gpu="1",UUID="GPU-def",device="nvidia1",modelName="A100"} 2048
DCGM_FI_DEV_FB_TOTAL{gpu="1",UUID="GPU-def",device="nvidia1",modelName="A100"} 81920
DCGM_FI_DEV_GPU_TEMP{gpu="1",UUID="GPU-def",device="nvidia1",modelName="A100"} 41
DCGM_FI_DRIVER_VERSION{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100",Driver_Version="535.104.05"} 1
DCGM_FI_CUDA_DRIVER_VERSION{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 12020
DCGM_FI_DEV_CUDA_COMPUTE_CAPABILITY{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100",cuda_compute_capability="8.0"} 0
DCGM_FI_DEV_PCI_BUSID{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100",pci_bus_id="00000000:3B:00.0"} 1
DCGM_FI_DEV_PCIE_LINK_GEN{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 4
DCGM_FI_DEV_PCIE_LINK_WIDTH{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 16
DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 42
`,
		},
		GPUHardwareInfos: []model.GPUHardwareInfo{
			{
				UUID:      "GPU-abc",
				Index:     "0",
				Product:   "A100",
				PCIEBusID: "00000000:3B:00.0",
				NUMANode:  "1",
			},
			{
				UUID:    "GPU-def",
				Index:   "1",
				Product: "A100",
			},
		},
		EndpointAllocations: []model.EndpointAllocation{
			{
				Workspace:  "default",
				Cluster:    "static-a",
				Endpoint:   "chat",
				InstanceID: "chat-replica-a",
				ReplicaID:  "replica-a",
				NodeID:     "head-0",
				Devices: []v1.DeviceAllocation{
					{
						UUID:          "GPU-abc",
						Product:       "NVIDIA_A100",
						MemoryMiB:     81920,
						CoreUnits:     100,
						NodeID:        "head-0",
						UsedMemoryMiB: 4096,
					},
				},
			},
		},
	})

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",target="node-exporter"} 0`)
	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",target="accelerator-exporter"} 1`)
	assert.Contains(t, output, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 0.87`)
	assert.Contains(t, output, `neutree_accelerator_memory_used_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 45097156608`)
	assert.Contains(t, output, `neutree_accelerator_memory_total_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 85899345920`)
	assert.Contains(t, output, `neutree_accelerator_temperature_celsius{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 72`)
	assert.Contains(t, output, `neutree_accelerator_pcie_tx_bytes_total{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 1024`)
	assert.Contains(t, output, `neutree_accelerator_pcie_rx_bytes_total{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 2048`)
	assert.Contains(t, output, `neutree_node_accelerator_total{accelerator_type="nvidia_gpu",cluster_type="ray",node="head-0",product="A100"} 2`)
	assert.Contains(t, output, `neutree_node_accelerator_allocated{accelerator_type="nvidia_gpu",cluster_type="ray",node="head-0",product="A100"} 1`)
	assert.Contains(t, output, `neutree_node_accelerator_free{accelerator_type="nvidia_gpu",cluster_type="ray",node="head-0",product="A100"} 1`)
	assert.Contains(t, output, `neutree_node_accelerator_info{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 1`)
	assert.Contains(t, output, `neutree_node_accelerator_hardware_info{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",memory_total_bytes="unknown",node="head-0",numa_node="1",pcie_bus_id="00000000:3B:00.0",pcie_generation="unknown",pcie_width="unknown",product="A100"} 1`)
	assert.Contains(t, output, `neutree_node_accelerator_nvidia_info{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",architecture="unknown",cluster_type="ray",cuda_capability="unknown",cuda_driver_version="unknown",driver_version="unknown",node="head-0",nvlink="unknown",nvswitch="unknown",product="A100"} 1`)
	allocationLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",endpoint="chat",instance_id="chat-replica-a",node="head-0",product="NVIDIA_A100",replica="replica-a",vdevice_index="0"`
	allocationInfoLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",endpoint="chat",instance_id="chat-replica-a",node="head-0",physical_vram_usage="42 GiB / 80 GiB",product="NVIDIA_A100",replica="replica-a",vdevice_index="0",vram_usage="4 GiB / 80 GiB"`
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_allocation{`+allocationInfoLabels+`} 1`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_allocated_bytes{`+allocationLabels+`} 85899345920`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+allocationLabels+`} 4294967296`)
	assert.NotContains(t, output, "neutree_node_gpu_allocation_info")
	assert.NotContains(t, output, "gpu_uuid=")
	assert.NotContains(t, output, "neutree_metrics_mapping_supported")
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

func TestNormalizerDerivesEndpointReplicaGPUUsageFromUniqueDCGMAllocation(t *testing.T) {
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
			Body: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 62
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="A100"} 2048
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920
`,
		},
		EndpointAllocations: []model.EndpointAllocation{
			{
				Workspace:  "default",
				Cluster:    "k8s-a",
				Endpoint:   "chat",
				InstanceID: "chat-abc",
				ReplicaID:  "chat-abc",
				NodeID:     "node-a",
				Devices: []v1.DeviceAllocation{
					{
						UUID:      "GPU-abc",
						Product:   "NVIDIA_A100",
						MemoryMiB: 81920,
						CoreUnits: 100,
						NodeID:    "node-a",
					},
				},
			},
		},
	})

	commonLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="kubernetes",endpoint="chat",instance_id="chat-abc",` +
		`node="node-a",product="NVIDIA_A100",replica="chat-abc",vdevice_index="0"`
	allocationInfoLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="kubernetes",endpoint="chat",instance_id="chat-abc",` +
		`node="node-a",physical_vram_usage="2 GiB / 80 GiB",product="NVIDIA_A100",replica="chat-abc",vdevice_index="0",vram_usage="2 GiB / 80 GiB"`
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_allocation{`+allocationInfoLabels+`} 1`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_allocated_bytes{`+commonLabels+`} 85899345920`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+commonLabels+`} 2147483648`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_utilization_ratio{`+commonLabels+`} 0.62`)
}

func TestNormalizerDoesNotDeriveEndpointReplicaGPUUsageForSharedDCGMAllocation(t *testing.T) {
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
			Body: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 62
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="A100"} 2048
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920
`,
		},
		EndpointAllocations: []model.EndpointAllocation{
			{
				Workspace:  "default",
				Cluster:    "k8s-a",
				Endpoint:   "chat-a",
				InstanceID: "chat-a-abc",
				ReplicaID:  "chat-a-abc",
				NodeID:     "node-a",
				Devices: []v1.DeviceAllocation{
					{UUID: "GPU-abc", Product: "NVIDIA_A100", MemoryMiB: 40960, CoreUnits: 50, NodeID: "node-a"},
				},
			},
			{
				Workspace:  "default",
				Cluster:    "k8s-a",
				Endpoint:   "chat-b",
				InstanceID: "chat-b-abc",
				ReplicaID:  "chat-b-abc",
				NodeID:     "node-a",
				Devices: []v1.DeviceAllocation{
					{UUID: "GPU-abc", Product: "NVIDIA_A100", MemoryMiB: 40960, CoreUnits: 50, NodeID: "node-a"},
				},
			},
		},
	})

	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_allocation{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-a"`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_allocation{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-b"`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-a"`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-b"`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-a"`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-b"`)
}

func TestNormalizerUsesExplicitEndpointReplicaGPUUsageForSharedAllocationDisplay(t *testing.T) {
	chatAUsedBytes := 4096.0 * 1024 * 1024
	chatBUsedBytes := 3072.0 * 1024 * 1024
	chatAUtilization := 0.25
	chatBUtilization := 0.75

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
			Body: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="Tesla T4"} 62
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="Tesla T4"} 12288
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="Tesla T4"} 15360
`,
		},
		EndpointAllocations: []model.EndpointAllocation{
			{
				Workspace:  "default",
				Cluster:    "k8s-a",
				Endpoint:   "chat-a",
				InstanceID: "chat-a-abc",
				ReplicaID:  "chat-a-abc",
				NodeID:     "node-a",
				Devices: []v1.DeviceAllocation{
					{UUID: "GPU-abc", Product: "Tesla-T4", MemoryMiB: 8192, CoreUnits: 50, NodeID: "node-a"},
				},
			},
			{
				Workspace:  "default",
				Cluster:    "k8s-a",
				Endpoint:   "chat-b",
				InstanceID: "chat-b-abc",
				ReplicaID:  "chat-b-abc",
				NodeID:     "node-a",
				Devices: []v1.DeviceAllocation{
					{UUID: "GPU-abc", Product: "Tesla-T4", MemoryMiB: 7168, CoreUnits: 50, NodeID: "node-a"},
				},
			},
		},
		EndpointReplicaGPUUsages: []model.EndpointReplicaGPUUsage{
			{
				Endpoint:         "chat-a",
				InstanceID:       "chat-a-abc",
				ReplicaID:        "chat-a-abc",
				NodeID:           "node-a",
				GPUUUID:          "GPU-abc",
				AcceleratorIndex: "0",
				VDeviceIndex:     "0",
				Product:          "Tesla-T4",
				MemoryUsedBytes:  &chatAUsedBytes,
				UtilizationRatio: &chatAUtilization,
			},
			{
				Endpoint:         "chat-b",
				InstanceID:       "chat-b-abc",
				ReplicaID:        "chat-b-abc",
				NodeID:           "node-a",
				GPUUUID:          "GPU-abc",
				AcceleratorIndex: "0",
				VDeviceIndex:     "0",
				Product:          "Tesla-T4",
				MemoryUsedBytes:  &chatBUsedBytes,
				UtilizationRatio: &chatBUtilization,
			},
		},
	})

	chatACommonLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="kubernetes",endpoint="chat-a",instance_id="chat-a-abc",` +
		`node="node-a",product="Tesla-T4",replica="chat-a-abc",vdevice_index="0"`
	chatBCommonLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="kubernetes",endpoint="chat-b",instance_id="chat-b-abc",` +
		`node="node-a",product="Tesla-T4",replica="chat-b-abc",vdevice_index="0"`
	chatAAllocationInfoLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="kubernetes",endpoint="chat-a",instance_id="chat-a-abc",` +
		`node="node-a",physical_vram_usage="12 GiB / 15 GiB",product="Tesla-T4",replica="chat-a-abc",vdevice_index="0",vram_usage="4 GiB / 8 GiB"`
	chatBAllocationInfoLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="kubernetes",endpoint="chat-b",instance_id="chat-b-abc",` +
		`node="node-a",physical_vram_usage="12 GiB / 15 GiB",product="Tesla-T4",replica="chat-b-abc",vdevice_index="0",vram_usage="3 GiB / 7 GiB"`
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_allocation{`+chatAAllocationInfoLabels+`} 1`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_allocation{`+chatBAllocationInfoLabels+`} 1`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_allocated_bytes{`+chatACommonLabels+`} 8589934592`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+chatACommonLabels+`} 4294967296`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_utilization_ratio{`+chatACommonLabels+`} 0.25`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_allocated_bytes{`+chatBCommonLabels+`} 7516192768`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+chatBCommonLabels+`} 3221225472`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_utilization_ratio{`+chatBCommonLabels+`} 0.75`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{accelerator_index="unknown"`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_utilization_ratio{accelerator_index="unknown"`)
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

func TestGPUHardwareInfosFromAcceleratorMetrics(t *testing.T) {
	raw := `DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920
DCGM_FI_DRIVER_VERSION{gpu="0",UUID="GPU-abc",modelName="A100",Driver_Version="535.104.05"} 1
DCGM_FI_CUDA_DRIVER_VERSION{gpu="0",UUID="GPU-abc",modelName="A100"} 12020
DCGM_FI_DEV_CUDA_COMPUTE_CAPABILITY{gpu="0",UUID="GPU-abc",modelName="A100",cuda_compute_capability="8.0"} 0
DCGM_FI_DEV_PCI_BUSID{gpu="0",UUID="GPU-abc",modelName="A100",pci_bus_id="00000000:3B:00.0"} 1
DCGM_FI_DEV_PCIE_LINK_GEN{gpu="0",UUID="GPU-abc",modelName="A100"} 4
DCGM_FI_DEV_PCIE_LINK_WIDTH{gpu="0",UUID="GPU-abc",modelName="A100"} 16
DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 1
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
	assert.Equal(t, "present", infos[0].NVLink)
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

func TestNormalizerDoesNotOutputNodeGPUInventoryWithoutGPUUtilGate(t *testing.T) {
	output := normalizeForTest(NormalizeRequest{
		Labels: testLabels(),
		AcceleratorExporter: &model.ScrapeResult{
			Target: TargetAcceleratorExporter,
			Up:     true,
			Body: `# TYPE DCGM_FI_DEV_FB_TOTAL gauge
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920`,
		},
		GPUHardwareInfos: []model.GPUHardwareInfo{
			{UUID: "GPU-abc", Index: "0", Product: "A100", MemoryTotalMiB: "81920"},
		},
	})

	assert.Contains(t, output, `neutree_accelerator_memory_total_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 85899345920`)
	assert.NotContains(t, output, "neutree_node_accelerator_hardware_info")
	assert.NotContains(t, output, "neutree_node_accelerator_info")
	assert.NotContains(t, output, "neutree_node_accelerator_total")
	assert.NotContains(t, output, "neutree_node_accelerator_allocated")
	assert.NotContains(t, output, "neutree_node_accelerator_free")
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
