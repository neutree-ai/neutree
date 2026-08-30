package nvidia

import (
	"context"
	"strconv"
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
	Init() bool
	Shutdown()
	DeviceCount() (int, bool)
	Device(index int) (nvidiaNVMLHardwareDevice, bool)
	DriverVersion() (string, bool)
	CUDADriverVersion() (string, bool)
}

type nvidiaNVMLHardwareDevice interface {
	UUID() (string, bool)
	Index() (int, bool)
	MinorNumber() (int, bool)
	Product() (string, bool)
	Architecture() (uint32, bool)
	CUDACapability() (int, int, bool)
	PCIEBusID() (string, bool)
	PCIEGeneration() (int, bool)
	PCIEWidth() (int, bool)
	MemoryTotalBytes() (uint64, bool)
	NUMANode() (int, bool)
}

func (p nvidiaNVMLHardwareInfoProvider) GPUHardwareInfos(ctx context.Context) ([]nvidiaHardwareInfo, error) {
	client := p.client

	if client == nil {
		client = newNvidiaNVMLHardwareClient()
	}

	if client == nil || !client.Init() {
		return nil, nil
	}

	defer client.Shutdown()

	count, ok := client.DeviceCount()
	if !ok || count <= 0 {
		return nil, nil
	}

	driverVersion, _ := client.DriverVersion()
	cudaDriverVersion, _ := client.CUDADriverVersion()
	infos := make([]nvidiaHardwareInfo, 0, count)

	for index := 0; index < count; index++ {
		if err := ctx.Err(); err != nil {
			return infos, nil
		}

		device, ok := client.Device(index)
		if !ok {
			continue
		}

		info := nvidiaHardwareInfo{}
		if value, ok := device.UUID(); ok {
			info.UUID = nvidiaCleanHardwareValue(value)
		}

		if info.UUID == "" {
			continue
		}

		info.DriverVersion = driverVersion
		info.CUDADriverVersion = cudaDriverVersion
		nvidiaApplyNVMLDeviceHardwareInfo(&info, device)

		if info.Index == "" {
			info.Index = strconv.Itoa(index)
		}

		if info.NUMANode == "" {
			info.NUMANode = nvidiaNUMANodeFromSysFS(p.SysFSRoot, info.PCIEBusID)
		}

		infos = append(infos, info)
	}

	return infos, nil
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

	if value, ok := device.MemoryTotalBytes(); ok && value > 0 {
		info.MemoryTotalMiB = strconv.FormatUint(value/nvidiaBytesPerMiB, 10)
	}

	if value, ok := device.NUMANode(); ok && value >= 0 {
		info.NUMANode = strconv.Itoa(value)
	}
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
