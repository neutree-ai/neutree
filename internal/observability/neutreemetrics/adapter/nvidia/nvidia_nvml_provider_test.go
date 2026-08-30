package nvidia

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNvidiaNVMLHardwareInfoProviderBuildsInventory(t *testing.T) {
	provider := nvidiaNVMLHardwareInfoProvider{client: fakeNvidiaNVMLHardwareClient{
		initOK: true,
		driver: "550.54",
		cuda:   "12.8",
		devices: []nvidiaNVMLHardwareDevice{fakeNvidiaNVMLHardwareDevice{
			uuid:         "GPU-a",
			index:        2,
			minorNumber:  5,
			product:      "NVIDIA L20",
			architecture: 8,
			cudaMajor:    8,
			cudaMinor:    9,
			pciBusID:     "0000:05:00.0",
			pcieGen:      4,
			pcieWidth:    16,
			memoryBytes:  46068 * nvidiaBytesPerMiB,
			numaNode:     0,
		}},
	}}

	infos, err := provider.GPUHardwareInfos(context.Background())

	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "GPU-a", infos[0].UUID)
	assert.Equal(t, "2", infos[0].Index)
	assert.Equal(t, "5", infos[0].MinorNumber)
	assert.Equal(t, "NVIDIA L20", infos[0].Product)
	assert.Equal(t, "Ada", infos[0].Architecture)
	assert.Equal(t, "8.9", infos[0].CUDACapability)
	assert.Equal(t, "550.54", infos[0].DriverVersion)
	assert.Equal(t, "12.8", infos[0].CUDADriverVersion)
	assert.Equal(t, "46068", infos[0].MemoryTotalMiB)
	assert.Equal(t, "0000:05:00.0", infos[0].PCIEBusID)
	assert.Equal(t, "4", infos[0].PCIEGeneration)
	assert.Equal(t, "16", infos[0].PCIEWidth)
	assert.Equal(t, "0", infos[0].NUMANode)
}

func TestNvidiaNVMLHardwareInfoProviderUnavailableReturnsNoInventory(t *testing.T) {
	infos, err := (nvidiaNVMLHardwareInfoProvider{client: fakeNvidiaNVMLHardwareClient{}}).GPUHardwareInfos(context.Background())

	require.NoError(t, err)
	assert.Empty(t, infos)
}

type fakeNvidiaNVMLHardwareClient struct {
	initOK  bool
	driver  string
	cuda    string
	devices []nvidiaNVMLHardwareDevice
}

func (c fakeNvidiaNVMLHardwareClient) Init() bool { return c.initOK }

func (fakeNvidiaNVMLHardwareClient) Shutdown() {}

func (c fakeNvidiaNVMLHardwareClient) DeviceCount() (int, bool) {
	return len(c.devices), true
}

func (c fakeNvidiaNVMLHardwareClient) Device(index int) (nvidiaNVMLHardwareDevice, bool) {
	if index < 0 || index >= len(c.devices) {
		return nil, false
	}

	return c.devices[index], true
}

func (c fakeNvidiaNVMLHardwareClient) DriverVersion() (string, bool) {
	return c.driver, c.driver != ""
}

func (c fakeNvidiaNVMLHardwareClient) CUDADriverVersion() (string, bool) {
	return c.cuda, c.cuda != ""
}

type fakeNvidiaNVMLHardwareDevice struct {
	uuid         string
	index        int
	minorNumber  int
	product      string
	architecture uint32
	cudaMajor    int
	cudaMinor    int
	pciBusID     string
	pcieGen      int
	pcieWidth    int
	memoryBytes  uint64
	numaNode     int
}

func (d fakeNvidiaNVMLHardwareDevice) UUID() (string, bool) { return d.uuid, d.uuid != "" }

func (d fakeNvidiaNVMLHardwareDevice) Index() (int, bool) { return d.index, true }

func (d fakeNvidiaNVMLHardwareDevice) MinorNumber() (int, bool) { return d.minorNumber, true }

func (d fakeNvidiaNVMLHardwareDevice) Product() (string, bool) { return d.product, d.product != "" }

func (d fakeNvidiaNVMLHardwareDevice) Architecture() (uint32, bool) { return d.architecture, true }

func (d fakeNvidiaNVMLHardwareDevice) CUDACapability() (int, int, bool) {
	return d.cudaMajor, d.cudaMinor, true
}

func (d fakeNvidiaNVMLHardwareDevice) PCIEBusID() (string, bool) { return d.pciBusID, d.pciBusID != "" }

func (d fakeNvidiaNVMLHardwareDevice) PCIEGeneration() (int, bool) { return d.pcieGen, true }

func (d fakeNvidiaNVMLHardwareDevice) PCIEWidth() (int, bool) { return d.pcieWidth, true }

func (d fakeNvidiaNVMLHardwareDevice) MemoryTotalBytes() (uint64, bool) { return d.memoryBytes, true }

func (d fakeNvidiaNVMLHardwareDevice) NUMANode() (int, bool) { return d.numaNode, true }
