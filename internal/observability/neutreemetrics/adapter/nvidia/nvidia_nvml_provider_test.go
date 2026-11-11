package nvidia

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/klog/v2"
)

func TestNvidiaNVMLHardwareInfoProviderBuildsInventory(t *testing.T) {
	cases := []struct {
		name        string
		unsupported bool
	}{
		{name: "supported hardware properties"},
		{name: "unsupported optional properties remain empty and healthy", unsupported: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			device := fakeNvidiaNVMLHardwareDevice{
				uuid: "GPU-a", index: 2, minorNumber: 5, product: "NVIDIA L20",
				architecture: 8, cudaMajor: 8, cudaMinor: 9, pciBusID: "0000:05:00.0",
				pcieGen: 4, pcieWidth: 16, memoryBytes: 46068 * nvidiaBytesPerMiB, numaNode: 0,
			}
			want := nvidiaHardwareInfo{
				UUID: "GPU-a", Index: "2", MinorNumber: "5", Product: "NVIDIA L20",
				Architecture: "Ada", CUDACapability: "8.9", PCIEBusID: "0000:05:00.0",
				PCIEGeneration: "4", PCIEWidth: "16", MemoryTotalMiB: "46068", NUMANode: "0",
				DriverVersion: "550.54", CUDADriverVersion: "12.8",
			}
			if tc.unsupported {
				device.cudaMajor, device.cudaMinor = -1, -1
				device.pcieGen, device.pcieWidth, device.numaNode = -1, -1, -1
				want.CUDACapability = ""
				want.PCIEGeneration, want.PCIEWidth, want.NUMANode = "", "", ""
			}
			provider := nvidiaNVMLHardwareInfoProvider{SysFSRoot: t.TempDir(), client: fakeNvidiaNVMLHardwareClient{
				initOK: true, driver: "550.54", cuda: "12.8", devices: []nvidiaNVMLHardwareDevice{device},
			}}

			infos, err := provider.GPUHardwareInfos(context.Background())

			require.NoError(t, err)
			assert.Equal(t, []nvidiaHardwareInfo{want}, infos)
		})
	}
}

func TestNvidiaNVMLHardwareInfoProviderReportsDiscoveryFailure(t *testing.T) {
	cases := []struct {
		name      string
		client    fakeNvidiaNVMLHardwareClient
		wantError string
	}{
		{name: "NVML initialization fails", wantError: "nvmlInit failed"},
		{
			name:      "device count query fails",
			client:    fakeNvidiaNVMLHardwareClient{initOK: true, countUnavailable: true},
			wantError: "nvmlDeviceGetCount",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := nvidiaNVMLHardwareInfoProvider{client: tc.client}
			infos, err := provider.GPUHardwareInfos(context.Background())

			require.ErrorContains(t, err, tc.wantError)
			assert.Empty(t, infos)
		})
	}
}

func TestNvidiaAdapterPreservesFailedSlotsAndLogs(t *testing.T) {
	cases := []struct {
		name              string
		handleUnavailable bool
		uuidUnavailable   bool
		memoryUnavailable bool
		wantUUID          string
		wantLogs          []string
	}{
		{
			name:              "GPU handle unavailable as reported by nvidia-smi",
			handleUnavailable: true,
			wantLogs:          []string{"nvmlDeviceGetHandleByIndex failed"},
		},
		{
			name:              "driver errors while reading GPU properties",
			uuidUnavailable:   true,
			memoryUnavailable: true,
			wantLogs:          []string{"nvmlDeviceGetUUID", "nvmlDeviceGetMemoryInfo"},
		},
		{
			name:              "memory query fails while UUID remains readable",
			memoryUnavailable: true,
			wantUUID:          "GPU-4",
			wantLogs:          []string{"nvmlDeviceGetMemoryInfo"},
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
				devices[i] = fakeNvidiaNVMLHardwareDevice{
					uuid: fmt.Sprintf("GPU-%d", i), index: i, minorNumber: i,
					product: "RTX 5090", pciBusID: fmt.Sprintf("0000:%02x:00.0", i),
				}
			}
			devices[4] = fakeNvidiaNVMLHardwareDevice{
				uuid: "GPU-4", index: 4, minorNumber: 4, product: "RTX 5090",
				pciBusID: "0000:98:00.0", uuidUnavailable: tc.uuidUnavailable,
				memoryUnavailable: tc.memoryUnavailable,
			}
			accel := &accelerator{provider: nvidiaNVMLHardwareInfoProvider{client: fakeNvidiaNVMLHardwareClient{
				initOK: true, devices: devices, deviceUnavailable: map[int]bool{4: tc.handleUnavailable},
				driver: "595.58.03", cuda: "13.1",
			}}}

			snapshot, err := accel.DiscoverHardware(context.Background())

			require.NoError(t, err)
			require.Len(t, snapshot.Accelerator.Devices, 8)
			assert.Equal(t, tc.wantUUID, snapshot.Accelerator.Devices[4].UUID)
			assert.Equal(t, "4", snapshot.Accelerator.Devices[4].ID)
			assert.False(t, snapshot.Accelerator.Devices[4].Healthy)
			assert.Equal(t, "GPU-7", snapshot.Accelerator.Devices[7].UUID)
			assert.Equal(t, "7", snapshot.Accelerator.Devices[7].ID)
			assert.True(t, snapshot.Accelerator.Devices[7].Healthy)
			klog.Flush()
			for _, message := range tc.wantLogs {
				assert.Contains(t, logs.String(), message)
			}
			if !tc.handleUnavailable {
				require.NotNil(t, snapshot.Accelerator.Devices[4].MinorNumber)
				assert.Equal(t, 4, *snapshot.Accelerator.Devices[4].MinorNumber)
				assert.Equal(t, "RTX 5090", snapshot.Accelerator.Devices[4].ProductName)
				assert.Equal(t, "0000:98:00.0", snapshot.Details[4].PCIEBusID)
			}
		})
	}
}

type fakeNvidiaNVMLHardwareClient struct {
	initOK            bool
	countUnavailable  bool
	deviceUnavailable map[int]bool
	driver            string
	cuda              string
	devices           []nvidiaNVMLHardwareDevice
}

func (c fakeNvidiaNVMLHardwareClient) Init() bool { return c.initOK }

func (fakeNvidiaNVMLHardwareClient) Shutdown() {}

func (c fakeNvidiaNVMLHardwareClient) DeviceCount() (int, bool) {
	return len(c.devices), !c.countUnavailable
}

func (c fakeNvidiaNVMLHardwareClient) Device(index int) (nvidiaNVMLHardwareDevice, bool) {
	if c.deviceUnavailable[index] {
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
	uuid              string
	uuidUnavailable   bool
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

func (d fakeNvidiaNVMLHardwareDevice) UUID() (string, bool) { return d.uuid, !d.uuidUnavailable }

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

func (d fakeNvidiaNVMLHardwareDevice) MemoryTotalBytes() (uint64, bool) {
	return d.memoryBytes, !d.memoryUnavailable
}

func (d fakeNvidiaNVMLHardwareDevice) NUMANode() (int, bool) { return d.numaNode, true }
