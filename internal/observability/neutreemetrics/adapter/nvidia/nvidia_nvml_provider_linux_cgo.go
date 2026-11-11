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
	return nvidiaNVMLProperty("nvmlSystemGetDriverVersion", version, ret, "")
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

// Unsupported properties are absent, not failures; numeric callers supply a sentinel that cannot be mistaken for a valid value.
func nvidiaNVMLProperty[T any](operation string, value T, ret nvml.Return, empty T) (T, bool) {
	if ret == nvml.ERROR_NOT_SUPPORTED {
		return empty, true
	}

	return value, nvidiaNVMLCallSucceeded(operation, ret)
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
	return nvidiaNVMLProperty("nvmlDeviceGetUUID", value, ret, "")
}

func (d realNvidiaNVMLHardwareDevice) Index() (int, bool) {
	value, ret := d.device.GetIndex()
	return nvidiaNVMLProperty("nvmlDeviceGetIndex", value, ret, -1)
}

func (d realNvidiaNVMLHardwareDevice) MinorNumber() (int, bool) {
	value, ret := d.device.GetMinorNumber()
	return nvidiaNVMLProperty("nvmlDeviceGetMinorNumber", value, ret, -1)
}

func (d realNvidiaNVMLHardwareDevice) Product() (string, bool) {
	value, ret := d.device.GetName()
	return nvidiaNVMLProperty("nvmlDeviceGetName", value, ret, "")
}

func (d realNvidiaNVMLHardwareDevice) Architecture() (uint32, bool) {
	value, ret := d.device.GetArchitecture()
	return nvidiaNVMLProperty("nvmlDeviceGetArchitecture", uint32(value), ret, 0)
}

func (d realNvidiaNVMLHardwareDevice) CUDACapability() (int, int, bool) {
	major, minor, ret := d.device.GetCudaComputeCapability()
	if ret == nvml.ERROR_NOT_SUPPORTED {
		return -1, -1, true
	}

	return major, minor, nvidiaNVMLCallSucceeded("nvmlDeviceGetCudaComputeCapability", ret)
}

func (d realNvidiaNVMLHardwareDevice) PCIEBusID() (string, bool) {
	value, ret := d.device.GetPciInfo()
	if ret == nvml.ERROR_NOT_SUPPORTED {
		return "", true
	}

	if !nvidiaNVMLCallSucceeded("nvmlDeviceGetPciInfo", ret) {
		return "", false
	}

	busID := nvidiaCStringFromInt8(value.BusId[:])
	if busID == "" {
		busID = nvidiaCStringFromInt8(value.BusIdLegacy[:])
	}

	return busID, busID != ""
}

func (d realNvidiaNVMLHardwareDevice) PCIEGeneration() (int, bool) {
	value, ret := d.device.GetMaxPcieLinkGeneration()
	return nvidiaNVMLProperty("nvmlDeviceGetMaxPcieLinkGeneration", value, ret, -1)
}

func (d realNvidiaNVMLHardwareDevice) PCIEWidth() (int, bool) {
	value, ret := d.device.GetMaxPcieLinkWidth()
	return nvidiaNVMLProperty("nvmlDeviceGetMaxPcieLinkWidth", value, ret, -1)
}

func (d realNvidiaNVMLHardwareDevice) MemoryTotalBytes() (uint64, bool) {
	value, ret := d.device.GetMemoryInfo()
	return nvidiaNVMLProperty("nvmlDeviceGetMemoryInfo", value.Total, ret, 0)
}

func (d realNvidiaNVMLHardwareDevice) NUMANode() (int, bool) {
	value, ret := d.device.GetNumaNodeId()
	return nvidiaNVMLProperty("nvmlDeviceGetNumaNodeId", value, ret, -1)
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
