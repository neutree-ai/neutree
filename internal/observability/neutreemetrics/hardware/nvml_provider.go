package hardware

import (
	"context"
	"strconv"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
)

const bytesPerMiB = 1024 * 1024

type NVMLGPUHardwareInfoProvider struct {
	SysFSRoot string
	client    nvmlGPUHardwareClient
}

type nvmlGPUHardwareClient interface {
	Init() bool
	Shutdown()
	DeviceCount() (int, bool)
	Device(index int) (nvmlGPUHardwareDevice, bool)
	DriverVersion() (string, bool)
	CUDADriverVersion() (string, bool)
}

type nvmlGPUHardwareDevice interface {
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

func (p NVMLGPUHardwareInfoProvider) GPUHardwareInfos(ctx context.Context) ([]model.GPUHardwareInfo, error) {
	client := p.client
	if client == nil {
		client = newNVMLGPUHardwareClient()
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
	infos := make([]model.GPUHardwareInfo, 0, count)

	for index := 0; index < count; index++ {
		if err := ctx.Err(); err != nil {
			return infos, nil
		}

		device, ok := client.Device(index)
		if !ok {
			continue
		}

		info := model.GPUHardwareInfo{}
		if value, ok := device.UUID(); ok {
			info.UUID = cleanHardwareValue(value)
		}
		if info.UUID == "" {
			continue
		}

		info.DriverVersion = driverVersion
		info.CUDADriverVersion = cudaDriverVersion
		applyNVMLDeviceHardwareInfo(&info, device)
		if info.Index == "" {
			info.Index = strconv.Itoa(index)
		}
		if info.NUMANode == "" {
			info.NUMANode = numaNodeFromSysFS(p.SysFSRoot, info.PCIEBusID)
		}

		infos = append(infos, info)
	}

	return infos, nil
}

func applyNVMLDeviceHardwareInfo(info *model.GPUHardwareInfo, device nvmlGPUHardwareDevice) {
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
		info.Architecture = nvmlArchitectureLabel(value)
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
		info.MemoryTotalMiB = strconv.FormatUint(value/bytesPerMiB, 10)
	}
	if value, ok := device.NUMANode(); ok && value >= 0 {
		info.NUMANode = strconv.Itoa(value)
	}
}

func nvmlArchitectureLabel(architecture uint32) string {
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
