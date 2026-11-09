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
	return version, nvidiaNVMLCallSucceeded("nvmlSystemGetDriverVersion", ret)
}

func (c *realNvidiaNVMLHardwareClient) CUDADriverVersion() (string, bool) {
	version, ret := c.api.SystemGetCudaDriverVersion_v2()
	if !nvidiaNVMLCallSucceeded("nvmlSystemGetCudaDriverVersion_v2", ret) {
		version, ret = c.api.SystemGetCudaDriverVersion()
		if !nvidiaNVMLCallSucceeded("nvmlSystemGetCudaDriverVersion", ret) {
			return "", false
		}
	}

	return nvidiaFormatCUDADriverVersion(float64(version)), true
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
	return value, nvidiaNVMLCallSucceeded("nvmlDeviceGetUUID", ret)
}

func (d realNvidiaNVMLHardwareDevice) Index() (int, bool) {
	value, ret := d.device.GetIndex()
	return value, nvidiaNVMLCallSucceeded("nvmlDeviceGetIndex", ret)
}

func (d realNvidiaNVMLHardwareDevice) MinorNumber() (int, bool) {
	value, ret := d.device.GetMinorNumber()
	return value, nvidiaNVMLCallSucceeded("nvmlDeviceGetMinorNumber", ret)
}

func (d realNvidiaNVMLHardwareDevice) Product() (string, bool) {
	value, ret := d.device.GetName()
	return value, nvidiaNVMLCallSucceeded("nvmlDeviceGetName", ret)
}

func (d realNvidiaNVMLHardwareDevice) Architecture() (uint32, bool) {
	value, ret := d.device.GetArchitecture()
	return uint32(value), nvidiaNVMLCallSucceeded("nvmlDeviceGetArchitecture", ret)
}

func (d realNvidiaNVMLHardwareDevice) CUDACapability() (int, int, bool) {
	major, minor, ret := d.device.GetCudaComputeCapability()
	return major, minor, nvidiaNVMLCallSucceeded("nvmlDeviceGetCudaComputeCapability", ret)
}

func (d realNvidiaNVMLHardwareDevice) PCIEBusID() (string, bool) {
	value, ret := d.device.GetPciInfo()
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
	return value, nvidiaNVMLCallSucceeded("nvmlDeviceGetMaxPcieLinkGeneration", ret)
}

func (d realNvidiaNVMLHardwareDevice) PCIEWidth() (int, bool) {
	value, ret := d.device.GetMaxPcieLinkWidth()
	return value, nvidiaNVMLCallSucceeded("nvmlDeviceGetMaxPcieLinkWidth", ret)
}

func (d realNvidiaNVMLHardwareDevice) MemoryTotalBytes() (uint64, bool) {
	value, ret := d.device.GetMemoryInfo()
	return value.Total, nvidiaNVMLCallSucceeded("nvmlDeviceGetMemoryInfo", ret)
}

func (d realNvidiaNVMLHardwareDevice) NUMANode() (int, bool) {
	value, ret := d.device.GetNumaNodeId()
	return value, nvidiaNVMLCallSucceeded("nvmlDeviceGetNumaNodeId", ret)
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
