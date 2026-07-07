package hardware

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestGPUHardwareInfosFromDCGMStaticHardwareMetrics(t *testing.T) {
	raw := `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="Tesla T4"} 87
DCGM_FI_DEV_NVML_INDEX{UUID="GPU-abc"} 3
DCGM_FI_SYSTEM_DRIVER_VERSION{UUID="GPU-abc",DCGM_FI_SYSTEM_DRIVER_VERSION="570.195.03"} 1
DCGM_FI_CUDA_DRIVER_VERSION{UUID="GPU-abc"} 12080
DCGM_FI_DEV_CUDA_COMPUTE_CAPABILITY{UUID="GPU-abc"} 75
DCGM_FI_DEV_PCI_BUSID{UUID="GPU-abc",DCGM_FI_DEV_PCI_BUSID="00000000:00:0A.0"} 1
DCGM_FI_DEV_PCIE_MAX_LINK_GEN{UUID="GPU-abc"} 3
DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH{UUID="GPU-abc"} 16
DCGM_FI_DEV_FB_TOTAL{UUID="GPU-abc"} 15360
`

	infos := gpuHardwareInfosFromAcceleratorMetrics(raw)

	require.Len(t, infos, 1)
	assert.Equal(t, model.GPUHardwareInfo{
		UUID:              "GPU-abc",
		Index:             "0",
		Product:           "Tesla T4",
		CUDACapability:    "7.5",
		DriverVersion:     "570.195.03",
		CUDADriverVersion: "12.8",
		MemoryTotalMiB:    "15360",
		PCIEBusID:         "00000000:00:0A.0",
		PCIEGeneration:    "3",
		PCIEWidth:         "16",
	}, infos[0])
	assert.Empty(t, infos[0].Architecture)
}

func TestNVMLGPUHardwareInfoProviderUsesClientFields(t *testing.T) {
	provider := NVMLGPUHardwareInfoProvider{
		client: fakeNVMLGPUHardwareClient{
			driverVersion:     "570.195.03",
			cudaDriverVersion: "12.8",
			devices: []fakeNVMLGPUHardwareDevice{
				{
					uuid:             "GPU-abc",
					index:            intPtr(0),
					minorNumber:      intPtr(3),
					product:          "Tesla T4",
					architecture:     uint32Ptr(6),
					cudaMajor:        intPtr(7),
					cudaMinor:        intPtr(5),
					pcieBusID:        "00000000:00:0A.0",
					pcieGeneration:   intPtr(3),
					pcieWidth:        intPtr(16),
					memoryTotalBytes: uint64Ptr(15360 * bytesPerMiB),
					numaNode:         intPtr(1),
				},
			},
		},
	}

	infos, err := provider.GPUHardwareInfos(context.Background())

	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, model.GPUHardwareInfo{
		UUID:              "GPU-abc",
		Index:             "0",
		MinorNumber:       "3",
		Product:           "Tesla T4",
		Architecture:      "Turing",
		CUDACapability:    "7.5",
		DriverVersion:     "570.195.03",
		CUDADriverVersion: "12.8",
		MemoryTotalMiB:    "15360",
		PCIEBusID:         "00000000:00:0A.0",
		PCIEGeneration:    "3",
		PCIEWidth:         "16",
		NUMANode:          "1",
	}, infos[0])
}

func TestNVMLGPUHardwareInfoProviderFallsBackToSysFSNUMA(t *testing.T) {
	root := t.TempDir()
	deviceDir := filepath.Join(root, "bus", "pci", "devices", "0000:00:0a.0")
	require.NoError(t, os.MkdirAll(deviceDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(deviceDir, "numa_node"), []byte("2\n"), 0600))

	provider := NVMLGPUHardwareInfoProvider{
		SysFSRoot: root,
		client: fakeNVMLGPUHardwareClient{
			devices: []fakeNVMLGPUHardwareDevice{
				{
					uuid:      "GPU-abc",
					pcieBusID: "00000000:00:0A.0",
				},
			},
		},
	}

	infos, err := provider.GPUHardwareInfos(context.Background())

	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "2", infos[0].NUMANode)
}

func TestNVMLGPUHardwareInfoProviderUnavailableReturnsNoInfos(t *testing.T) {
	provider := NVMLGPUHardwareInfoProvider{client: fakeNVMLGPUHardwareClient{initOK: boolPtr(false)}}

	infos, err := provider.GPUHardwareInfos(context.Background())

	require.NoError(t, err)
	assert.Empty(t, infos)
}

func TestNVMLArchitectureLabel(t *testing.T) {
	tests := map[uint32]string{
		2:  "Kepler",
		3:  "Maxwell",
		4:  "Pascal",
		5:  "Volta",
		6:  "Turing",
		7:  "Ampere",
		8:  "Ada",
		9:  "Hopper",
		10: "Blackwell",
		13: "Rubin",
		99: "",
	}

	for architecture, expected := range tests {
		assert.Equal(t, expected, nvmlArchitectureLabel(architecture))
	}
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

type fakeNVMLGPUHardwareClient struct {
	initOK            *bool
	driverVersion     string
	cudaDriverVersion string
	devices           []fakeNVMLGPUHardwareDevice
}

func (c fakeNVMLGPUHardwareClient) Init() bool {
	if c.initOK == nil {
		return true
	}

	return *c.initOK
}

func (c fakeNVMLGPUHardwareClient) Shutdown() {}

func (c fakeNVMLGPUHardwareClient) DeviceCount() (int, bool) {
	return len(c.devices), true
}

func (c fakeNVMLGPUHardwareClient) Device(index int) (nvmlGPUHardwareDevice, bool) {
	if index < 0 || index >= len(c.devices) {
		return nil, false
	}

	return c.devices[index], true
}

func (c fakeNVMLGPUHardwareClient) DriverVersion() (string, bool) {
	return c.driverVersion, c.driverVersion != ""
}

func (c fakeNVMLGPUHardwareClient) CUDADriverVersion() (string, bool) {
	return c.cudaDriverVersion, c.cudaDriverVersion != ""
}

type fakeNVMLGPUHardwareDevice struct {
	uuid             string
	index            *int
	minorNumber      *int
	product          string
	architecture     *uint32
	cudaMajor        *int
	cudaMinor        *int
	pcieBusID        string
	pcieGeneration   *int
	pcieWidth        *int
	memoryTotalBytes *uint64
	numaNode         *int
}

func (d fakeNVMLGPUHardwareDevice) UUID() (string, bool) {
	return d.uuid, d.uuid != ""
}

func (d fakeNVMLGPUHardwareDevice) Index() (int, bool) {
	if d.index == nil {
		return 0, false
	}

	return *d.index, true
}

func (d fakeNVMLGPUHardwareDevice) MinorNumber() (int, bool) {
	if d.minorNumber == nil {
		return 0, false
	}

	return *d.minorNumber, true
}

func (d fakeNVMLGPUHardwareDevice) Product() (string, bool) {
	return d.product, d.product != ""
}

func (d fakeNVMLGPUHardwareDevice) Architecture() (uint32, bool) {
	if d.architecture == nil {
		return 0, false
	}

	return *d.architecture, true
}

func (d fakeNVMLGPUHardwareDevice) CUDACapability() (int, int, bool) {
	if d.cudaMajor == nil || d.cudaMinor == nil {
		return 0, 0, false
	}

	return *d.cudaMajor, *d.cudaMinor, true
}

func (d fakeNVMLGPUHardwareDevice) PCIEBusID() (string, bool) {
	return d.pcieBusID, d.pcieBusID != ""
}

func (d fakeNVMLGPUHardwareDevice) PCIEGeneration() (int, bool) {
	if d.pcieGeneration == nil {
		return 0, false
	}

	return *d.pcieGeneration, true
}

func (d fakeNVMLGPUHardwareDevice) PCIEWidth() (int, bool) {
	if d.pcieWidth == nil {
		return 0, false
	}

	return *d.pcieWidth, true
}

func (d fakeNVMLGPUHardwareDevice) MemoryTotalBytes() (uint64, bool) {
	if d.memoryTotalBytes == nil {
		return 0, false
	}

	return *d.memoryTotalBytes, true
}

func (d fakeNVMLGPUHardwareDevice) NUMANode() (int, bool) {
	if d.numaNode == nil {
		return 0, false
	}

	return *d.numaNode, true
}

func boolPtr(value bool) *bool {
	return &value
}

func intPtr(value int) *int {
	return &value
}

func uint32Ptr(value uint32) *uint32 {
	return &value
}

func uint64Ptr(value uint64) *uint64 {
	return &value
}
