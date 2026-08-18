package adapter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
)

// TestNvidiaAdapterBuildMetricsMatchesLegacyDCGMSamples verifies the nvidia
// adapter (reference implementation) produces the same accelerator samples the
// legacy normalizer DCGM path produced, so existing DCGM assertions stay green.
func TestNvidiaAdapterBuildMetricsMatchesLegacyDCGMSamples(t *testing.T) {
	raw := `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 87
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
`

	result, err := (&nvidiaAccelerator{}).BuildMetrics(context.Background(), AcceleratorEvidence{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		ExporterText:    raw,
		ExporterUp:      true,
		Labels:          testLabels(),
		GPUHardwareInfos: []model.GPUHardwareInfo{
			{UUID: "GPU-abc", Index: "0", Product: "A100", PCIEBusID: "00000000:3B:00.0", NUMANode: "1"},
			{UUID: "GPU-def", Index: "1", Product: "A100"},
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
	require.NoError(t, err)

	output := formatSamples(result.Samples)

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

func TestNvidiaAdapterBuildMetricsProducesDeviceSnapshots(t *testing.T) {
	raw := `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 87
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 81920
`

	result, err := (&nvidiaAccelerator{}).BuildMetrics(context.Background(), AcceleratorEvidence{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		ExporterText:    raw,
		ExporterUp:      true,
		Labels:          testLabels(),
	})
	require.NoError(t, err)

	require.Len(t, result.DeviceSnapshots, 1)
	assert.Equal(t, "GPU-abc", result.DeviceSnapshots[0].UUID)
	assert.Equal(t, "A100", result.DeviceSnapshots[0].Product)
	assert.Equal(t, int64(81920), result.DeviceSnapshots[0].MemoryMiB)
	assert.Equal(t, "head-0", result.DeviceSnapshots[0].NodeID)
}

func TestNvidiaAdapterDerivesEndpointReplicaGPUUsageFromUniqueDCGMAllocation(t *testing.T) {
	raw := `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 62
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="A100"} 2048
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920
`

	result, err := (&nvidiaAccelerator{}).BuildMetrics(context.Background(), AcceleratorEvidence{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		ExporterText:    raw,
		ExporterUp:      true,
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
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
					{UUID: "GPU-abc", Product: "NVIDIA_A100", MemoryMiB: 81920, CoreUnits: 100, NodeID: "node-a"},
				},
			},
		},
	})
	require.NoError(t, err)

	output := formatSamples(result.Samples)
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

func TestNvidiaAdapterDoesNotDeriveEndpointReplicaGPUUsageForSharedDCGMAllocation(t *testing.T) {
	raw := `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 62
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="A100"} 2048
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920
`

	result, err := (&nvidiaAccelerator{}).BuildMetrics(context.Background(), AcceleratorEvidence{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		ExporterText:    raw,
		ExporterUp:      true,
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		EndpointAllocations: []model.EndpointAllocation{
			{
				Workspace:  "default",
				Cluster:    "k8s-a",
				Endpoint:   "chat-a",
				InstanceID: "chat-a-abc",
				ReplicaID:  "chat-a-abc",
				NodeID:     "node-a",
				Devices:    []v1.DeviceAllocation{{UUID: "GPU-abc", Product: "NVIDIA_A100", MemoryMiB: 40960, CoreUnits: 50, NodeID: "node-a"}},
			},
			{
				Workspace:  "default",
				Cluster:    "k8s-a",
				Endpoint:   "chat-b",
				InstanceID: "chat-b-abc",
				ReplicaID:  "chat-b-abc",
				NodeID:     "node-a",
				Devices:    []v1.DeviceAllocation{{UUID: "GPU-abc", Product: "NVIDIA_A100", MemoryMiB: 40960, CoreUnits: 50, NodeID: "node-a"}},
			},
		},
	})
	require.NoError(t, err)

	output := formatSamples(result.Samples)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_allocation{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-a"`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_allocation{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-b"`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-a"`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-b"`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-a"`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",endpoint="chat-b"`)
}

func TestNvidiaAdapterUsesExplicitEndpointReplicaGPUUsageForSharedAllocationDisplay(t *testing.T) {
	chatAUsedBytes := 4096.0 * 1024 * 1024
	chatBUsedBytes := 3072.0 * 1024 * 1024
	chatAUtilization := 0.25
	chatBUtilization := 0.75

	raw := `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="Tesla T4"} 62
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="Tesla T4"} 12288
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="Tesla T4"} 15360
`

	result, err := (&nvidiaAccelerator{}).BuildMetrics(context.Background(), AcceleratorEvidence{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		ExporterText:    raw,
		ExporterUp:      true,
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		EndpointAllocations: []model.EndpointAllocation{
			{
				Workspace:  "default",
				Cluster:    "k8s-a",
				Endpoint:   "chat-a",
				InstanceID: "chat-a-abc",
				ReplicaID:  "chat-a-abc",
				NodeID:     "node-a",
				Devices:    []v1.DeviceAllocation{{UUID: "GPU-abc", Product: "Tesla-T4", MemoryMiB: 8192, CoreUnits: 50, NodeID: "node-a"}},
			},
			{
				Workspace:  "default",
				Cluster:    "k8s-a",
				Endpoint:   "chat-b",
				InstanceID: "chat-b-abc",
				ReplicaID:  "chat-b-abc",
				NodeID:     "node-a",
				Devices:    []v1.DeviceAllocation{{UUID: "GPU-abc", Product: "Tesla-T4", MemoryMiB: 7168, CoreUnits: 50, NodeID: "node-a"}},
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
	require.NoError(t, err)

	output := formatSamples(result.Samples)
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

func TestNvidiaAdapterParsesDCGMLabelsWithSpaces(t *testing.T) {
	raw := `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="Tesla T4",Hostname="gpu-node"} 87
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="Tesla T4",Hostname="gpu-node"} 15360
`

	result, err := (&nvidiaAccelerator{}).BuildMetrics(context.Background(), AcceleratorEvidence{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		ExporterText:    raw,
		ExporterUp:      true,
		Labels:          testLabels(),
	})
	require.NoError(t, err)

	output := formatSamples(result.Samples)
	assert.Contains(t, output, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="Tesla T4"} 0.87`)
	assert.Contains(t, output, `neutree_accelerator_memory_total_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="Tesla T4"} 16106127360`)
}

func TestNvidiaAdapterDoesNotOutputNodeGPUInventoryWithoutGPUUtilGate(t *testing.T) {
	raw := `# TYPE DCGM_FI_DEV_FB_TOTAL gauge
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920`

	result, err := (&nvidiaAccelerator{}).BuildMetrics(context.Background(), AcceleratorEvidence{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		ExporterText:    raw,
		ExporterUp:      true,
		Labels:          testLabels(),
		GPUHardwareInfos: []model.GPUHardwareInfo{
			{UUID: "GPU-abc", Index: "0", Product: "A100", MemoryTotalMiB: "81920"},
		},
	})
	require.NoError(t, err)

	output := formatSamples(result.Samples)
	assert.Contains(t, output, `neutree_accelerator_memory_total_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 85899345920`)
	assert.NotContains(t, output, "neutree_node_accelerator_hardware_info")
	assert.NotContains(t, output, "neutree_node_accelerator_info")
	assert.NotContains(t, output, "neutree_node_accelerator_total")
	assert.NotContains(t, output, "neutree_node_accelerator_allocated")
	assert.NotContains(t, output, "neutree_node_accelerator_free")
}

func TestNvidiaAdapterEnrichesMultiPhysicalGPUUsageWhenVDeviceIndexDiffersFromAllocation(t *testing.T) {
	firstUsedBytes := 2048.0 * 1024 * 1024
	secondUsedBytes := 4096.0 * 1024 * 1024

	raw := `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="NVIDIA L20"} 62
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="NVIDIA L20"} 2048
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="NVIDIA L20"} 46068
DCGM_FI_DEV_GPU_UTIL{gpu="1",UUID="GPU-def",modelName="NVIDIA L20"} 63
DCGM_FI_DEV_FB_USED{gpu="1",UUID="GPU-def",modelName="NVIDIA L20"} 4096
DCGM_FI_DEV_FB_TOTAL{gpu="1",UUID="GPU-def",modelName="NVIDIA L20"} 46068
`

	result, err := (&nvidiaAccelerator{}).BuildMetrics(context.Background(), AcceleratorEvidence{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		ExporterText:    raw,
		ExporterUp:      true,
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
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
					{UUID: "GPU-abc", Product: "NVIDIA-L20", MemoryMiB: 23034, CoreUnits: 50, NodeID: "node-a"},
					{UUID: "GPU-def", Product: "NVIDIA-L20", MemoryMiB: 23034, CoreUnits: 50, NodeID: "node-a"},
				},
			},
		},
		EndpointReplicaGPUUsages: []model.EndpointReplicaGPUUsage{
			{
				Endpoint:        "chat",
				InstanceID:      "chat-abc",
				ReplicaID:       "chat-abc",
				NodeID:          "node-a",
				GPUUUID:         "GPU-abc",
				VDeviceIndex:    "0",
				MemoryUsedBytes: &firstUsedBytes,
			},
			{
				Endpoint:        "chat",
				InstanceID:      "chat-abc",
				ReplicaID:       "chat-abc",
				NodeID:          "node-a",
				GPUUUID:         "GPU-def",
				VDeviceIndex:    "1",
				MemoryUsedBytes: &secondUsedBytes,
			},
		},
	})
	require.NoError(t, err)

	output := formatSamples(result.Samples)
	firstLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="kubernetes",endpoint="chat",instance_id="chat-abc",` +
		`node="node-a",product="NVIDIA-L20",replica="chat-abc",vdevice_index="0"`
	secondLabels := `accelerator_index="1",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-def",` +
		`cluster_type="kubernetes",endpoint="chat",instance_id="chat-abc",` +
		`node="node-a",product="NVIDIA-L20",replica="chat-abc",vdevice_index="1"`
	unknownSecondProductLabels := `accelerator_index="1",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-def",` +
		`cluster_type="kubernetes",endpoint="chat",instance_id="chat-abc",` +
		`node="node-a",product="unknown"`
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+firstLabels+`} 2147483648`)
	assert.Contains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+secondLabels+`} 4294967296`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{accelerator_index="unknown"`)
	assert.NotContains(t, output, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+unknownSecondProductLabels)
}
