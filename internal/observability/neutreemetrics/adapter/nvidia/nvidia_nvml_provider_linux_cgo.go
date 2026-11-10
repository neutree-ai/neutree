//go:build linux && cgo

package nvidia

import (
	"fmt"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"k8s.io/klog/v2"
)

type realNvidiaNVMLHardwareClient struct {
	api nvml.Interface
}

type realNvidiaNVMLHardwareDevice struct {
	device nvml.Device
}

func newNvidiaNVMLHardwareClient() nvidiaNVMLHardwareClient {
	return &realNvidiaNVMLHardwareClient{api: nvml.New()}
}

func (c *realNvidiaNVMLHardwareClient) Init() bool {
	return nvidiaNVMLCallSucceeded("nvmlInit", c.api.Init())
}

func (c *realNvidiaNVMLHardwareClient) Shutdown() {
	nvidiaNVMLCallSucceeded("nvmlShutdown", c.api.Shutdown())
}

func (c *realNvidiaNVMLHardwareClient) DeviceCount() (int, bool) {
	count, ret := c.api.DeviceGetCount()
	return count, nvidiaNVMLCallSucceeded("nvmlDeviceGetCount", ret)
}

func (c *realNvidiaNVMLHardwareClient) Device(index int) (nvidiaNVMLHardwareDevice, bool) {
	device, ret := c.api.DeviceGetHandleByIndex(index)
	if !nvidiaNVMLCallSucceeded("nvmlDeviceGetHandleByIndex", ret) {
		return nil, false
	}

	return realNvidiaNVMLHardwareDevice{device: device}, true
}

func (c *realNvidiaNVMLHardwareClient) DriverVersion() (string, bool) {
	version, ret := c.api.SystemGetDriverVersion()
	if available, ok := nvidiaNVMLProperty("nvmlSystemGetDriverVersion", ret); !available {
		return "", ok
	}

	return version, true
}

func (c *realNvidiaNVMLHardwareClient) CUDADriverVersion() (string, bool) {
	version, ret := c.api.SystemGetCudaDriverVersion_v2()
	if ret != nvml.SUCCESS {
		v2Ret := ret
		version, ret = c.api.SystemGetCudaDriverVersion()

		if ret != nvml.SUCCESS && ret != nvml.ERROR_NOT_SUPPORTED {
			klog.Warningf("NVIDIA NVML CUDA driver version queries failed: nvmlSystemGetCudaDriverVersion_v2: %v (ret=%d); nvmlSystemGetCudaDriverVersion: %v (ret=%d)",
				v2Ret, v2Ret, ret, ret)

			return "", false
		}
	}

	if ret == nvml.ERROR_NOT_SUPPORTED {
		return "", true
	}

	return nvidiaFormatCUDADriverVersion(float64(version)), true
}

// nvidiaNVMLProperty distinguishes an absent, unsupported value from a failed query.
func nvidiaNVMLProperty(operation string, ret nvml.Return) (available, ok bool) {
	if ret == nvml.ERROR_NOT_SUPPORTED {
		return false, true
	}

	ok = nvidiaNVMLCallSucceeded(operation, ret)

	return ok, ok
}

func nvidiaNVMLCallSucceeded(operation string, ret nvml.Return) bool {
	if ret == nvml.SUCCESS {
		return true
	}

	klog.Warningf("NVIDIA NVML call failed: %s: %v (ret=%d)", operation, ret, ret)

	return false
}

func (d realNvidiaNVMLHardwareDevice) UUID() (string, bool) {
	value, ret := d.device.GetUUID()
	if available, ok := nvidiaNVMLProperty("nvmlDeviceGetUUID", ret); !available {
		return "", ok
	}

	return value, true
}

func (d realNvidiaNVMLHardwareDevice) Index() (int, bool) {
	value, ret := d.device.GetIndex()
	if available, ok := nvidiaNVMLProperty("nvmlDeviceGetIndex", ret); !available {
		return -1, ok
	}

	return value, true
}

func (d realNvidiaNVMLHardwareDevice) MinorNumber() (int, bool) {
	value, ret := d.device.GetMinorNumber()
	if available, ok := nvidiaNVMLProperty("nvmlDeviceGetMinorNumber", ret); !available {
		return -1, ok
	}

	return value, true
}

func (d realNvidiaNVMLHardwareDevice) Product() (string, bool) {
	value, ret := d.device.GetName()
	if available, ok := nvidiaNVMLProperty("nvmlDeviceGetName", ret); !available {
		return "", ok
	}

	return value, true
}

func (d realNvidiaNVMLHardwareDevice) Architecture() (uint32, bool) {
	value, ret := d.device.GetArchitecture()
	if available, ok := nvidiaNVMLProperty("nvmlDeviceGetArchitecture", ret); !available {
		return 0, ok
	}

	return uint32(value), true
}

func (d realNvidiaNVMLHardwareDevice) CUDACapability() (int, int, bool) {
	major, minor, ret := d.device.GetCudaComputeCapability()
	if available, ok := nvidiaNVMLProperty("nvmlDeviceGetCudaComputeCapability", ret); !available {
		return -1, -1, ok
	}

	return major, minor, true
}

func (d realNvidiaNVMLHardwareDevice) PCIEBusID() (string, bool) {
	value, ret := d.device.GetPciInfo()
	if available, ok := nvidiaNVMLProperty("nvmlDeviceGetPciInfo", ret); !available {
		return "", ok
	}

	busID := nvidiaCStringFromInt8(value.BusId[:])
	if busID == "" {
		busID = nvidiaCStringFromInt8(value.BusIdLegacy[:])
	}

	return busID, busID != ""
}

func (d realNvidiaNVMLHardwareDevice) PCIEGeneration() (int, bool) {
	value, ret := d.device.GetMaxPcieLinkGeneration()
	if available, ok := nvidiaNVMLProperty("nvmlDeviceGetMaxPcieLinkGeneration", ret); !available {
		return -1, ok
	}

	return value, true
}

func (d realNvidiaNVMLHardwareDevice) PCIEWidth() (int, bool) {
	value, ret := d.device.GetMaxPcieLinkWidth()
	if available, ok := nvidiaNVMLProperty("nvmlDeviceGetMaxPcieLinkWidth", ret); !available {
		return -1, ok
	}

	return value, true
}

func (d realNvidiaNVMLHardwareDevice) MemoryTotalBytes() (uint64, bool) {
	value, ret := d.device.GetMemoryInfo()
	if available, ok := nvidiaNVMLProperty("nvmlDeviceGetMemoryInfo", ret); !available {
		return 0, ok
	}

	return value.Total, true
}

func (d realNvidiaNVMLHardwareDevice) NUMANode() (int, bool) {
	value, ret := d.device.GetNumaNodeId()
	if available, ok := nvidiaNVMLProperty("nvmlDeviceGetNumaNodeId", ret); !available {
		return -1, ok
	}

	return value, true
}

func nvidiaCStringFromInt8(value []int8) string {
	bytes := make([]byte, 0, len(value))

	for _, char := range value {
		if char == 0 {
			break
		}

		bytes = append(bytes, byte(char))
	}

	return string(bytes)
}

func nvidiaFormatCUDADriverVersion(value float64) string {
	version := int64(value)
	if version <= 0 {
		return ""
	}

	major := version / 1000
	minor := (version % 1000) / 10

	if major <= 0 {
		return ""
	}

	return fmt.Sprintf("%d.%d", major, minor)
}
