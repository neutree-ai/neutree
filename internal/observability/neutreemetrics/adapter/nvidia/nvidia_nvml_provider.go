package nvidia

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

const nvidiaBytesPerMiB = 1024 * 1024

type nvidiaHardwareInfoProvider interface {
	GPUHardwareInfos(context.Context) ([]nvidiaHardwareInfo, error)
}

type nvidiaHardwareInfoProviderFunc func(context.Context) ([]nvidiaHardwareInfo, error)

func (f nvidiaHardwareInfoProviderFunc) GPUHardwareInfos(ctx context.Context) ([]nvidiaHardwareInfo, error) {
	return f(ctx)
}

type nvidiaNVMLHardwareInfoProvider struct {
	SysFSRoot string
	client    nvidiaNVMLHardwareClient
}

type nvidiaNVMLHardwareClient interface {
	Init() error
	Shutdown()
	DeviceCount() (int, error)
	Device(index int) (nvidiaNVMLHardwareDevice, error)
	DriverVersion() (string, bool)
	CUDADriverVersion() (string, bool)
}

type nvidiaNVMLHardwareDevice interface {
	UUID() (string, error)
	Index() (int, bool)
	MinorNumber() (int, bool)
	Product() (string, bool)
	Architecture() (uint32, bool)
	CUDACapability() (int, int, bool)
	PCIEBusID() (string, bool)
	PCIEGeneration() (int, bool)
	PCIEWidth() (int, bool)
	MemoryTotalBytes() (uint64, error)
	NUMANode() (int, bool)
}

func (p nvidiaNVMLHardwareInfoProvider) GPUHardwareInfos(ctx context.Context) ([]nvidiaHardwareInfo, error) {
	client := p.client

	if client == nil {
		client = newNvidiaNVMLHardwareClient()
	}

	if client == nil {
		return nil, fmt.Errorf("NVML hardware discovery is unavailable on this platform")
	}

	if err := client.Init(); err != nil {
		return nil, err
	}

	defer client.Shutdown()

	count, err := client.DeviceCount()
	if err != nil {
		return nil, err
	}

	if count < 0 {
		return nil, fmt.Errorf("NVML returned invalid device count %d", count)
	}

	driverVersion, _ := client.DriverVersion()
	cudaDriverVersion, _ := client.CUDADriverVersion()
	infos := make([]nvidiaHardwareInfo, 0, count)

	for index := 0; index < count; index++ {
		if err := ctx.Err(); err != nil {
			return infos, err
		}

		info := nvidiaHardwareInfo{Index: strconv.Itoa(index)}
		device, err := client.Device(index)

		if err != nil {
			info.Unhealthy = true

			klog.Warningf("Unable to discover NVIDIA GPU at index %d: %v; preserving the device slot without identity", index, err)

			infos = append(infos, info)

			continue
		}

		value, err := device.UUID()
		if err != nil {
			info.Unhealthy = true

			klog.Warningf("Unable to read NVIDIA GPU UUID at index %d: %v; preserving the device slot", index, err)

			infos = append(infos, info)

			continue
		}

		info.UUID = nvidiaCleanHardwareValue(value)

		if info.UUID == "" {
			info.Unhealthy = true

			klog.Warningf("NVML returned an empty UUID for NVIDIA GPU at index %d", index)

			infos = append(infos, info)

			continue
		}

		info.DriverVersion = driverVersion
		info.CUDADriverVersion = cudaDriverVersion
		nvidiaApplyNVMLDeviceHardwareInfo(&info, device)

		if info.NUMANode == "" {
			info.NUMANode = nvidiaNUMANodeFromSysFS(p.SysFSRoot, info.PCIEBusID)
		}

		infos = append(infos, info)
	}

	return infos, ctx.Err()
}

func nvidiaApplyNVMLDeviceHardwareInfo(info *nvidiaHardwareInfo, device nvidiaNVMLHardwareDevice) {
	if value, ok := device.Index(); ok {
		info.Index = strconv.Itoa(value)
	}

	if value, ok := device.MinorNumber(); ok {
		info.MinorNumber = strconv.Itoa(value)
	}

	if value, ok := device.Product(); ok {
		info.Product = value
	}

	if value, ok := device.Architecture(); ok {
		info.Architecture = nvidiaNVMLArchitectureLabel(value)
	}

	if major, minor, ok := device.CUDACapability(); ok {
		info.CUDACapability = strconv.Itoa(major) + "." + strconv.Itoa(minor)
	}

	if value, ok := device.PCIEBusID(); ok {
		info.PCIEBusID = value
	}

	if value, ok := device.PCIEGeneration(); ok {
		info.PCIEGeneration = strconv.Itoa(value)
	}

	if value, ok := device.PCIEWidth(); ok {
		info.PCIEWidth = strconv.Itoa(value)
	}

	if value, err := device.MemoryTotalBytes(); err != nil {
		klog.Warningf("Unable to read NVIDIA GPU memory for UUID=%q index=%q: %v", info.UUID, info.Index, err)

		if nvidiaIsGPUUnavailableError(err) {
			info.Unhealthy = true
		}
	} else if value > 0 {
		info.MemoryTotalMiB = strconv.FormatUint(value/nvidiaBytesPerMiB, 10)
	}

	if value, ok := device.NUMANode(); ok && value >= 0 {
		info.NUMANode = strconv.Itoa(value)
	}
}

func nvidiaIsGPUUnavailableError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ERROR_GPU_IS_LOST")
}

func nvidiaNVMLArchitectureLabel(architecture uint32) string {
	switch architecture {
	case 2:
		return "Kepler"
	case 3:
		return "Maxwell"
	case 4:
		return "Pascal"
	case 5:
		return "Volta"
	case 6:
		return "Turing"
	case 7:
		return "Ampere"
	case 8:
		return "Ada"
	case 9:
		return "Hopper"
	case 10:
		return "Blackwell"
	case 13:
		return "Rubin"
	default:
		return ""
	}
}
