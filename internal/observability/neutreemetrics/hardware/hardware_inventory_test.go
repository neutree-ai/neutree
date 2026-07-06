package hardware

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNvidiaSMIGPUHardwareCSV(t *testing.T) {
	raw := `0, GPU-abc, NVIDIA A100-SXM4-80GB, 535.104.05, 8.0, 00000000:3B:00.0, 4, 16, 81920
1, GPU-def, NVIDIA A100-SXM4-80GB, 535.104.05, [Not Supported], 00000000:86:00.0, N/A, N/A, 81920`

	infos := parseNvidiaSMIGPUHardwareCSV(raw, nvidiaSMIFullHardwareFields)

	require.Len(t, infos, 2)
	assert.Equal(t, model.GPUHardwareInfo{
		UUID:           "GPU-abc",
		Index:          "0",
		Product:        "NVIDIA A100-SXM4-80GB",
		CUDACapability: "8.0",
		DriverVersion:  "535.104.05",
		MemoryTotalMiB: "81920",
		PCIEBusID:      "00000000:3B:00.0",
		PCIEGeneration: "4",
		PCIEWidth:      "16",
	}, infos[0])
	assert.Equal(t, "GPU-def", infos[1].UUID)
	assert.Empty(t, infos[1].CUDACapability)
	assert.Empty(t, infos[1].PCIEGeneration)
	assert.Empty(t, infos[1].PCIEWidth)
}

func TestGPUHardwareInfosFromAcceleratorMetricsPrefersMaxPCIeCapability(t *testing.T) {
	raw := `DCGM_FI_DEV_PCIE_LINK_GEN{gpu="0",UUID="GPU-abc",modelName="L20"} 1
DCGM_FI_DEV_PCIE_LINK_WIDTH{gpu="0",UUID="GPU-abc",modelName="L20"} 8
DCGM_FI_DEV_PCIE_MAX_LINK_GEN{gpu="0",UUID="GPU-abc",modelName="L20"} 4
DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH{gpu="0",UUID="GPU-abc",modelName="L20"} 16
`

	infos := gpuHardwareInfosFromAcceleratorMetrics(raw)

	require.Len(t, infos, 1)
	assert.Equal(t, "GPU-abc", infos[0].UUID)
	assert.Equal(t, "4", infos[0].PCIEGeneration)
	assert.Equal(t, "16", infos[0].PCIEWidth)
}

func TestParseNvidiaSMICUDAVersion(t *testing.T) {
	raw := `| NVIDIA-SMI 535.104.05             Driver Version: 535.104.05   CUDA Version: 12.2     |`

	assert.Equal(t, "12.2", parseNvidiaSMICUDAVersion(raw))
	assert.Empty(t, parseNvidiaSMICUDAVersion("no cuda version"))
}

func TestParseNvidiaSMIProductArchitecturesXML(t *testing.T) {
	raw := `<?xml version="1.0" ?>
<nvidia_smi_log>
	<gpu id="00000000:31:00.0">
		<product_name>NVIDIA L20</product_name>
		<uuid>GPU-abc</uuid>
		<product_architecture>Ada Lovelace</product_architecture>
	</gpu>
	<gpu id="00000000:4B:00.0">
		<product_name>NVIDIA A100-SXM4-80GB</product_name>
		<uuid>GPU-def</uuid>
		<product_architecture>Ampere</product_architecture>
	</gpu>
</nvidia_smi_log>`

	architecturesByUUID := parseNvidiaSMIProductArchitecturesXML(raw)

	assert.Equal(t, map[string]string{
		"GPU-abc": "Ada Lovelace",
		"GPU-def": "Ampere",
	}, architecturesByUUID)
	assert.Nil(t, parseNvidiaSMIProductArchitecturesXML("not xml"))
}

func TestParseNvidiaSMIProductArchitecturesXMLFromQueryOutput(t *testing.T) {
	raw := `<?xml version="1.0" ?>
<!DOCTYPE nvidia_smi_log SYSTEM "nvsmi_device_v12.dtd">
<nvidia_smi_log>
	<gpu id="00000000:00:0A.0">
		<product_name>Tesla T4</product_name>
		<product_architecture>Turing</product_architecture>
		<uuid>GPU-7df37ae3-c725-bb48-c43b-7854629083ac</uuid>
		<minor_number>0</minor_number>
	</gpu>
</nvidia_smi_log>`

	architecturesByUUID := parseNvidiaSMIProductArchitecturesXML(raw)
	detailsByUUID := parseNvidiaSMIProductDetailsXML(raw)

	assert.Equal(t, map[string]string{
		"GPU-7df37ae3-c725-bb48-c43b-7854629083ac": "Turing",
	}, architecturesByUUID)
	assert.Equal(t, nvidiaSMIProductDetails{
		MinorNumber:  "0",
		Architecture: "Turing",
	}, detailsByUUID["GPU-7df37ae3-c725-bb48-c43b-7854629083ac"])
}

func TestFormatCUDAComputeCapability(t *testing.T) {
	assert.Equal(t, "8.0", formatCUDAComputeCapability(float64(8<<16)))
	assert.Equal(t, "8.0", formatCUDAComputeCapability(float64(8<<32)))
	assert.Equal(t, "8.0", formatCUDAComputeCapability(80))
}

func TestNUMANodeFromSysFS(t *testing.T) {
	root := t.TempDir()
	deviceDir := filepath.Join(root, "bus", "pci", "devices", "0000:3b:00.0")
	require.NoError(t, os.MkdirAll(deviceDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(deviceDir, "numa_node"), []byte("1\n"), 0600))

	assert.Equal(t, "0000:3b:00.0", sysFSPCIDeviceID("00000000:3B:00.0"))
	assert.Equal(t, "1", numaNodeFromSysFS(root, "00000000:3B:00.0"))
	assert.Empty(t, numaNodeFromSysFS(root, ""))
}

func TestMergeGPUHardwareInfosUsesFallbackOnlyForMissingFields(t *testing.T) {
	primary := []model.GPUHardwareInfo{
		{
			UUID:              "GPU-abc",
			Product:           "A100",
			DriverVersion:     "535.104.05",
			CUDADriverVersion: "12.2",
		},
	}
	fallback := []model.GPUHardwareInfo{
		{
			UUID:              "GPU-abc",
			Product:           "NVIDIA A100-SXM4-80GB",
			MinorNumber:       "3",
			Architecture:      "Ampere",
			CUDACapability:    "8.0",
			DriverVersion:     "fallback-driver",
			CUDADriverVersion: "fallback-cuda",
			PCIEBusID:         "00000000:3B:00.0",
			NUMANode:          "1",
		},
	}

	merged := mergeGPUHardwareInfos(primary, fallback)

	require.Len(t, merged, 1)
	assert.Equal(t, "A100", merged[0].Product)
	assert.Equal(t, "Ampere", merged[0].Architecture)
	assert.Equal(t, "535.104.05", merged[0].DriverVersion)
	assert.Equal(t, "12.2", merged[0].CUDADriverVersion)
	assert.Equal(t, "8.0", merged[0].CUDACapability)
	assert.Equal(t, "3", merged[0].MinorNumber)
	assert.Equal(t, "00000000:3B:00.0", merged[0].PCIEBusID)
	assert.Equal(t, "1", merged[0].NUMANode)
}

func TestMergeGPUHardwareInfosPrefersHigherPCIeCapability(t *testing.T) {
	primary := []model.GPUHardwareInfo{
		{
			UUID:           "GPU-abc",
			PCIEGeneration: "1",
			PCIEWidth:      "8",
		},
	}
	fallback := []model.GPUHardwareInfo{
		{
			UUID:           "GPU-abc",
			PCIEGeneration: "4",
			PCIEWidth:      "16",
		},
	}

	merged := mergeGPUHardwareInfos(primary, fallback)

	require.Len(t, merged, 1)
	assert.Equal(t, "4", merged[0].PCIEGeneration)
	assert.Equal(t, "16", merged[0].PCIEWidth)
}
