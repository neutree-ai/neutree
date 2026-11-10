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

	if client == nil {
		return nil, fmt.Errorf("NVML hardware discovery is unavailable on this platform")
	}

	if !client.Init() {
		return nil, fmt.Errorf("nvmlInit failed")
	}

	defer client.Shutdown()

	count, ok := client.DeviceCount()
	if !ok {
		err := fmt.Errorf("nvmlDeviceGetCount failed")
		klog.Warning(err)

		return nil, err
	}

	driverVersion, driverOK := client.DriverVersion()
	cudaDriverVersion, cudaOK := client.CUDADriverVersion()

	infos := make([]nvidiaHardwareInfo, count)
	for index := range infos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		info := nvidiaHardwareInfo{
			Index: strconv.Itoa(index), DriverVersion: driverVersion,
			CUDADriverVersion: cudaDriverVersion, Unhealthy: !driverOK || !cudaOK,
		}
		device, ok := client.Device(index)

		if !ok {
			info.Unhealthy = true

			klog.Warningf("Unable to discover NVIDIA GPU at index %d: nvmlDeviceGetHandleByIndex failed; preserving the device slot", index)

			infos[index] = info

			continue
		}

		if err := nvidiaApplyNVMLDeviceHardwareInfo(&info, device); err != nil {
			info.Unhealthy = true

			klog.Warning(err)
		}

		if info.NUMANode == "" {
			info.NUMANode = nvidiaNUMANodeFromSysFS(p.SysFSRoot, info.PCIEBusID)
		}

		infos[index] = info
	}

	return infos, nil
}

func nvidiaApplyNVMLDeviceHardwareInfo(info *nvidiaHardwareInfo, device nvidiaNVMLHardwareDevice) error {
	var failures []string

	if value, ok := device.UUID(); ok && nvidiaCleanHardwareValue(value) != "" {
		info.UUID = nvidiaCleanHardwareValue(value)
	} else {
		failures = append(failures, "nvmlDeviceGetUUID")
	}

	if value, ok := device.Index(); ok {
		if value >= 0 {
			info.Index = strconv.Itoa(value)
		}
	} else {
		failures = append(failures, "nvmlDeviceGetIndex")
	}

	if value, ok := device.MinorNumber(); ok {
		if value >= 0 {
			info.MinorNumber = strconv.Itoa(value)
		}
	} else {
		failures = append(failures, "nvmlDeviceGetMinorNumber")
	}

	if value, ok := device.Product(); ok {
		info.Product = value
	} else {
		failures = append(failures, "nvmlDeviceGetName")
	}

	if value, ok := device.Architecture(); ok {
		info.Architecture = nvidiaNVMLArchitectureLabel(value)
	} else {
		failures = append(failures, "nvmlDeviceGetArchitecture")
	}

	if major, minor, ok := device.CUDACapability(); ok {
		if major >= 0 && minor >= 0 {
			info.CUDACapability = strconv.Itoa(major) + "." + strconv.Itoa(minor)
		}
	} else {
		failures = append(failures, "nvmlDeviceGetCudaComputeCapability")
	}

	if value, ok := device.PCIEBusID(); ok {
		info.PCIEBusID = value
	} else {
		failures = append(failures, "nvmlDeviceGetPciInfo")
	}

	if value, ok := device.PCIEGeneration(); ok {
		if value >= 0 {
			info.PCIEGeneration = strconv.Itoa(value)
		}
	} else {
		failures = append(failures, "nvmlDeviceGetMaxPcieLinkGeneration")
	}

	if value, ok := device.PCIEWidth(); ok {
		if value >= 0 {
			info.PCIEWidth = strconv.Itoa(value)
		}
	} else {
		failures = append(failures, "nvmlDeviceGetMaxPcieLinkWidth")
	}

	if value, ok := device.MemoryTotalBytes(); ok {
		if value > 0 {
			info.MemoryTotalMiB = strconv.FormatUint(value/nvidiaBytesPerMiB, 10)
		}
	} else {
		failures = append(failures, "nvmlDeviceGetMemoryInfo")
	}

	if value, ok := device.NUMANode(); ok {
		if value >= 0 {
			info.NUMANode = strconv.Itoa(value)
		}
	} else {
		failures = append(failures, "nvmlDeviceGetNumaNodeId")
	}

	if len(failures) > 0 {
		return fmt.Errorf("NVIDIA GPU UUID=%q index=%q: failed NVML calls: %s", info.UUID, info.Index, strings.Join(failures, ", "))
	}

	return nil
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
