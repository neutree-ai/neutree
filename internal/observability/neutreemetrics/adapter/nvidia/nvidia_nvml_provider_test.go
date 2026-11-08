package nvidia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/klog/v2"
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
	provider := nvidiaNVMLHardwareInfoProvider{client: fakeNvidiaNVMLHardwareClient{}}
	infos, err := provider.GPUHardwareInfos(context.Background())

	require.Error(t, err)
	assert.Empty(t, infos)
	assert.Error(t, (&accelerator{provider: provider}).ValidateHardware(context.Background()))
}

func TestNvidiaNVMLHardwareInfoProviderReportsDeviceCountFailure(t *testing.T) {
	countErr := errors.New("nvmlDeviceGetCount: ERROR_UNKNOWN (ret=999)")
	provider := nvidiaNVMLHardwareInfoProvider{client: fakeNvidiaNVMLHardwareClient{initOK: true, countErr: countErr}}

	infos, err := provider.GPUHardwareInfos(context.Background())

	require.ErrorIs(t, err, countErr)
	assert.Empty(t, infos)
}

func TestNvidiaAdapterPreservesFailedSlotsAndLogs(t *testing.T) {
	cases := []struct {
		name      string
		handleErr error
		uuidErr   error
		wantLog   string
	}{
		{
			name:      "GPU handle unavailable as reported by nvidia-smi",
			handleErr: errors.New("nvmlDeviceGetHandleByIndex: ERROR_UNKNOWN (ret=999)"),
			wantLog:   "index 4: nvmlDeviceGetHandleByIndex: ERROR_UNKNOWN (ret=999)",
		},
		{
			name:    "driver error while reading UUID",
			uuidErr: errors.New("nvmlDeviceGetUUID: ERROR_UNKNOWN (ret=999)"),
			wantLog: "index 4: nvmlDeviceGetUUID: ERROR_UNKNOWN (ret=999)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			klog.LogToStderr(false)
			klog.SetOutput(&logs)
			t.Cleanup(func() {
				klog.Flush()
				klog.SetOutput(os.Stderr)
				klog.LogToStderr(true)
			})

			devices := make([]nvidiaNVMLHardwareDevice, 8)
			for i := range devices {
				devices[i] = fakeNvidiaNVMLHardwareDevice{uuid: fmt.Sprintf("GPU-%d", i), index: i, minorNumber: i}
			}
			devices[4] = fakeNvidiaNVMLHardwareDevice{uuid: "GPU-4", index: 4, minorNumber: 4, uuidErr: tc.uuidErr}
			accel := &accelerator{provider: nvidiaNVMLHardwareInfoProvider{client: fakeNvidiaNVMLHardwareClient{
				initOK: true, devices: devices, deviceErrors: map[int]error{4: tc.handleErr},
			}}}

			snapshot, err := accel.DiscoverHardware(context.Background())

			require.NoError(t, err)
			require.Len(t, snapshot.Accelerator.Devices, 8)
			assert.Empty(t, snapshot.Accelerator.Devices[4].UUID)
			assert.Equal(t, "4", snapshot.Accelerator.Devices[4].ID)
			assert.False(t, snapshot.Accelerator.Devices[4].Healthy)
			assert.Equal(t, "GPU-7", snapshot.Accelerator.Devices[7].UUID)
			assert.Equal(t, "7", snapshot.Accelerator.Devices[7].ID)
			assert.True(t, snapshot.Accelerator.Devices[7].Healthy)
			klog.Flush()
			assert.Contains(t, logs.String(), tc.wantLog)
			assert.NoError(t, accel.ValidateHardware(context.Background()), "a single failed GPU does not prevent startup")
		})
	}
}

func TestNvidiaNVMLHardwareInfoProviderKeepsIdentityWhenMemoryIsLost(t *testing.T) {
	accel := &accelerator{provider: nvidiaNVMLHardwareInfoProvider{client: fakeNvidiaNVMLHardwareClient{
		initOK: true,
		devices: []nvidiaNVMLHardwareDevice{fakeNvidiaNVMLHardwareDevice{
			uuid: "GPU-lost", memoryUnavailable: true,
		}},
	}}}

	snapshot, err := accel.DiscoverHardware(context.Background())

	require.NoError(t, err)
	require.Len(t, snapshot.Accelerator.Devices, 1)
	device := snapshot.Accelerator.Devices[0]
	assert.Equal(t, "GPU-lost", device.UUID)
	assert.Equal(t, "0", device.ID)
	require.NotNil(t, device.MinorNumber)
	assert.Equal(t, 0, *device.MinorNumber)
	assert.False(t, device.Healthy)
}

type fakeNvidiaNVMLHardwareClient struct {
	initOK       bool
	countErr     error
	deviceErrors map[int]error
	driver       string
	cuda         string
	devices      []nvidiaNVMLHardwareDevice
}

func (c fakeNvidiaNVMLHardwareClient) Init() error {
	if !c.initOK {
		return errors.New("nvmlInit: ERROR_LIBRARY_NOT_FOUND (ret=12)")
	}
	return nil
}

func (fakeNvidiaNVMLHardwareClient) Shutdown() {}

func (c fakeNvidiaNVMLHardwareClient) DeviceCount() (int, error) {
	return len(c.devices), c.countErr
}

func (c fakeNvidiaNVMLHardwareClient) Device(index int) (nvidiaNVMLHardwareDevice, error) {
	if err := c.deviceErrors[index]; err != nil {
		return nil, err
	}
	if index < 0 || index >= len(c.devices) {
		return nil, errors.New("invalid index")
	}

	return c.devices[index], nil
}

func (c fakeNvidiaNVMLHardwareClient) DriverVersion() (string, bool) {
	return c.driver, c.driver != ""
}

func (c fakeNvidiaNVMLHardwareClient) CUDADriverVersion() (string, bool) {
	return c.cuda, c.cuda != ""
}

type fakeNvidiaNVMLHardwareDevice struct {
	uuid              string
	uuidErr           error
	index             int
	minorNumber       int
	product           string
	architecture      uint32
	cudaMajor         int
	cudaMinor         int
	pciBusID          string
	pcieGen           int
	pcieWidth         int
	memoryBytes       uint64
	memoryUnavailable bool
	numaNode          int
}

func (d fakeNvidiaNVMLHardwareDevice) UUID() (string, error) { return d.uuid, d.uuidErr }

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

func (d fakeNvidiaNVMLHardwareDevice) MemoryTotalBytes() (uint64, error) {
	if d.memoryUnavailable {
		return d.memoryBytes, errors.New("nvmlDeviceGetMemoryInfo: ERROR_GPU_IS_LOST (ret=15)")
	}

	return d.memoryBytes, nil
}

func (d fakeNvidiaNVMLHardwareDevice) NUMANode() (int, bool) { return d.numaNode, true }
