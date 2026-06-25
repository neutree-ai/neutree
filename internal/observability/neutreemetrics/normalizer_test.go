package neutreemetrics

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizerNormalizeNodeMetrics(t *testing.T) {
	output := (&Normalizer{}).Normalize(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: ScrapeResult{
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

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",static_node_cluster="static-a",target="node-exporter",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_ready{cluster_type="ray",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_cpu_seconds_total{cluster_type="ray",cpu="0",mode="idle",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 100`)
	assert.Contains(t, output, `neutree_node_cpu_seconds_total{cluster_type="ray",cpu="0",mode="user",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 20`)
	assert.Contains(t, output, `neutree_node_memory_total_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 17179869184`)
	assert.Contains(t, output, `neutree_node_memory_available_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 6442450944`)
	assert.Contains(t, output, `neutree_node_memory_used_bytes{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 10737418240`)
	assert.Contains(t, output, `neutree_node_load1{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="node-exporter",static_node_cluster="static-a",workspace="default"} 2.5`)
}

func TestNormalizerNormalizesAcceleratorExporterAndEndpointAllocations(t *testing.T) {
	output := (&Normalizer{}).Normalize(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: ScrapeResult{
			Target: TargetNodeExporter,
			Up:     false,
		},
		AcceleratorExporter: &ScrapeResult{
			Target: TargetAcceleratorExporter,
			Up:     true,
			Body: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 87
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 1024
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 81920
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 72
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
		GPUHardwareInfos: []GPUHardwareInfo{
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
		EndpointAllocations: []EndpointAllocation{
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

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",static_node_cluster="static-a",target="node-exporter",workspace="default"} 0`)
	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",static_node_cluster="static-a",target="accelerator-exporter",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_gpu_utilization_ratio{cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",model="A100",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 0.87`)
	assert.Contains(t, output, `neutree_gpu_memory_used_bytes{cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",model="A100",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 1073741824`)
	assert.Contains(t, output, `neutree_gpu_memory_total_bytes{cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",model="A100",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 85899345920`)
	assert.Contains(t, output, `neutree_gpu_temperature_celsius{cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",model="A100",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 72`)
	assert.Contains(t, output, `neutree_node_gpu_total{accelerator_type="nvidia_gpu",cluster_type="ray",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="A100",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 2`)
	assert.Contains(t, output, `neutree_node_gpu_allocated{accelerator_type="nvidia_gpu",cluster_type="ray",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="A100",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_gpu_free{accelerator_type="nvidia_gpu",cluster_type="ray",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="A100",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_gpu_info{accelerator_type="nvidia_gpu",cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="A100",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_gpu_hardware_info{accelerator_type="nvidia_gpu",architecture="unknown",cluster_type="ray",cuda_capability="unknown",cuda_driver_version="unknown",driver_version="unknown",gpu_index="0",gpu_uuid="GPU-abc",memory_total_mib="unknown",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",numa_node="1",nvlink="unknown",nvswitch="unknown",pcie_bus_id="00000000:3B:00.0",pcie_generation="unknown",pcie_width="unknown",product="A100",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_endpoint_replica_gpu_allocation{cluster_type="ray",endpoint="chat",gpu_uuid="GPU-abc",instance_id="chat-replica-a",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="NVIDIA_A100",replica_id="replica-a",resource_mode="physical_gpu",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_endpoint_replica_gpu_memory_allocated_bytes{cluster_type="ray",endpoint="chat",gpu_uuid="GPU-abc",instance_id="chat-replica-a",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="NVIDIA_A100",replica_id="replica-a",resource_mode="physical_gpu",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 85899345920`)
	assert.Contains(t, output, `neutree_endpoint_replica_gpu_memory_used_bytes{cluster_type="ray",endpoint="chat",gpu_uuid="GPU-abc",instance_id="chat-replica-a",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="NVIDIA_A100",replica_id="replica-a",resource_mode="physical_gpu",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 4294967296`)
	assert.Contains(t, output, `neutree_node_gpu_allocation{cluster_type="ray",endpoint="chat",gpu_uuid="GPU-abc",instance_id="chat-replica-a",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="NVIDIA_A100",replica="replica-a",replica_id="replica-a",resource_mode="physical_gpu",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_gpu_allocation_info{accelerator_type="nvidia_gpu",cluster_type="ray",endpoint="chat",endpoint_replica="chat / replica-a",gpu_index="0",gpu_uuid="GPU-abc",instance_id="chat-replica-a",neutree_cluster="static-a",node="head-0",node_gpu="head-0 / GPU 0",node_ip="10.0.0.10",node_role="head",physical_vram="1.0 GiB / 80.0 GiB",product="NVIDIA_A100",replica="replica-a",replica_id="replica-a",resource_mode="physical_gpu",source="neutree-node-agent",static_node_cluster="static-a",vram="4.0 GiB / 80.0 GiB",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_gpu_allocation_info{accelerator_type="nvidia_gpu",cluster_type="ray",endpoint="-",endpoint_replica="-",gpu_index="1",gpu_uuid="GPU-def",instance_id="-",neutree_cluster="static-a",node="head-0",node_gpu="head-0 / GPU 1",node_ip="10.0.0.10",node_role="head",physical_vram="2.0 GiB / 80.0 GiB",product="A100",replica="-",replica_id="-",resource_mode="physical_gpu",source="neutree-node-agent",static_node_cluster="static-a",vram="-",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_gpu_allocation_memory_allocated_bytes{cluster_type="ray",endpoint="chat",gpu_uuid="GPU-abc",instance_id="chat-replica-a",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="NVIDIA_A100",replica="replica-a",replica_id="replica-a",resource_mode="physical_gpu",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 85899345920`)
	assert.Contains(t, output, `neutree_node_gpu_allocation_memory_used_bytes{cluster_type="ray",endpoint="chat",gpu_uuid="GPU-abc",instance_id="chat-replica-a",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="NVIDIA_A100",replica="replica-a",replica_id="replica-a",resource_mode="physical_gpu",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 4294967296`)
	assert.NotContains(t, output, "neutree_metrics_mapping_supported")
}

func TestNormalizerNormalizesEndpointReplicaRuntimeUsage(t *testing.T) {
	usageBytes := 1024.0
	workingSetBytes := 768.0
	cpuLimitCores := 2.5
	memoryLimitBytes := 2048.0

	output := (&Normalizer{}).Normalize(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: ScrapeResult{
			Target: TargetNodeExporter,
			Up:     false,
		},
		EndpointReplicaRuntimeUsages: []EndpointReplicaRuntimeUsage{
			{
				Workspace:             "default",
				Cluster:               "static-a",
				Endpoint:              "chat",
				InstanceID:            "actor-a",
				ReplicaID:             "replica-a",
				NodeID:                "head-0",
				Deployment:            "Backend",
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

	commonLabels := `cluster_type="ray",container="engine",container_id="docker-abc",deployment="Backend",` +
		`endpoint="chat",engine="vllm",engine_version="v0.17.1",instance_id="actor-a",` +
		`neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",` +
		`replica="replica-a",replica_id="replica-a",source="neutree-node-agent",` +
		`static_node_cluster="static-a",workspace="default"`
	assert.Contains(t, output, `neutree_endpoint_replica_cpu_usage_seconds_total{`+commonLabels+`} 12.5`)
	assert.Contains(t, output, `neutree_endpoint_replica_memory_usage_bytes{`+commonLabels+`} 1024`)
	assert.Contains(t, output, `neutree_endpoint_replica_memory_working_set_bytes{`+commonLabels+`} 768`)
	assert.Contains(t, output, `neutree_endpoint_replica_cpu_limit_cores{`+commonLabels+`} 2.5`)
	assert.Contains(t, output, `neutree_endpoint_replica_memory_limit_bytes{`+commonLabels+`} 2048`)
}

func TestNormalizerMarksRuntimeUsageScrapeState(t *testing.T) {
	output := (&Normalizer{}).Normalize(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: ScrapeResult{
			Target: TargetNodeExporter,
			Up:     true,
		},
		RuntimeUsage: &ScrapeResult{
			Target: TargetRuntimeUsage,
			Up:     false,
		},
	})

	assert.Contains(t, output, `neutree_metrics_scrape_up{cluster_type="ray",node="head-0",node_ip="10.0.0.10",node_role="head",source="neutree-node-agent",static_node_cluster="static-a",target="runtime-usage",workspace="default"} 0`)
}

func TestNormalizerPreservesHAMiVGPUResourceMode(t *testing.T) {
	output := (&Normalizer{}).Normalize(NormalizeRequest{
		Labels: testLabels(),
		NodeExporter: ScrapeResult{
			Target: TargetNodeExporter,
			Up:     true,
		},
		AcceleratorExporter: &ScrapeResult{
			Target: TargetAcceleratorExporter,
			Up:     true,
			Body: `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="Tesla T4"} 87
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="Tesla T4"} 15360
`,
		},
		EndpointAllocations: []EndpointAllocation{
			{
				Workspace:  "default",
				Cluster:    "static-a",
				Endpoint:   "chat",
				InstanceID: "pod-a",
				ReplicaID:  "pod-a",
				NodeID:     "head-0",
				Devices: []v1.DeviceAllocation{
					{
						UUID:         "GPU-abc",
						Product:      "Tesla-T4",
						MemoryMiB:    7680,
						CoreUnits:    50,
						NodeID:       "head-0",
						ResourceMode: v1.DeviceAllocationResourceModeHAMiVGPU,
					},
				},
			},
		},
	})

	assert.Contains(t, output, `neutree_node_gpu_allocation{cluster_type="ray",endpoint="chat",gpu_uuid="GPU-abc",instance_id="pod-a",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="Tesla-T4",replica="pod-a",replica_id="pod-a",resource_mode="hami_vgpu",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
	assert.Contains(t, output, `neutree_node_gpu_allocation_info{accelerator_type="nvidia_gpu",cluster_type="ray",endpoint="chat",endpoint_replica="chat / pod-a",gpu_index="0",gpu_uuid="GPU-abc",instance_id="pod-a",neutree_cluster="static-a",node="head-0",node_gpu="head-0 / GPU 0",node_ip="10.0.0.10",node_role="head",physical_vram="- / 15.0 GiB",product="Tesla-T4",replica="pod-a",replica_id="pod-a",resource_mode="hami_vgpu",source="neutree-node-agent",static_node_cluster="static-a",vram="- / 7.5 GiB",workspace="default"} 1`)
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

	infos := gpuHardwareInfosFromAcceleratorMetrics(raw)

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

	output := (&Normalizer{}).Normalize(NormalizeRequest{
		Labels: testLabels(),
		AcceleratorExporter: &ScrapeResult{
			Target: TargetAcceleratorExporter,
			Up:     true,
			Body:   raw,
		},
	})

	assert.Contains(t, output, `neutree_gpu_utilization_ratio{cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",model="Tesla T4",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 0.87`)
	assert.Contains(t, output, `neutree_gpu_memory_total_bytes{cluster_type="ray",gpu_index="0",gpu_uuid="GPU-abc",model="Tesla T4",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",source="accelerator-exporter",static_node_cluster="static-a",workspace="default"} 16106127360`)

	snapshot := snapshotFromAcceleratorMetrics(raw)
	require.NotNil(t, snapshot)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), snapshot.Accelerator.Type)
	require.Len(t, snapshot.Accelerator.Devices, 1)
	assert.Equal(t, "GPU-abc", snapshot.Accelerator.Devices[0].UUID)
	assert.Equal(t, "Tesla T4", snapshot.Accelerator.Devices[0].ProductName)
	assert.Equal(t, int64(15360), snapshot.Accelerator.Devices[0].MemoryMiB)
}

func testLabels() CanonicalLabels {
	return CanonicalLabels{
		Workspace:         "default",
		StaticNodeCluster: "static-a",
		ClusterType:       "ray",
		Node:              "head-0",
		NodeIP:            "10.0.0.10",
		NodeRole:          "head",
	}
}
