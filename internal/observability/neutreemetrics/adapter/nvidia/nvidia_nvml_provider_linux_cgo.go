//go:build linux && cgo

package nvidia

import (
	"fmt"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
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

func (c *realNvidiaNVMLHardwareClient) Init() error {
	return nvidiaNVMLHardwareError("nvmlInit", c.api.Init())
}

func (c *realNvidiaNVMLHardwareClient) Shutdown() {
	_ = c.api.Shutdown()
}

func (c *realNvidiaNVMLHardwareClient) DeviceCount() (int, error) {
	count, ret := c.api.DeviceGetCount()
	return count, nvidiaNVMLHardwareError("nvmlDeviceGetCount", ret)
}

func (c *realNvidiaNVMLHardwareClient) Device(index int) (nvidiaNVMLHardwareDevice, error) {
	device, ret := c.api.DeviceGetHandleByIndex(index)
	if err := nvidiaNVMLHardwareError("nvmlDeviceGetHandleByIndex", ret); err != nil {
		return nil, err
	}

	return realNvidiaNVMLHardwareDevice{device: device}, nil
}

func (c *realNvidiaNVMLHardwareClient) DriverVersion() (string, bool) {
	version, ret := c.api.SystemGetDriverVersion()
	return version, ret == nvml.SUCCESS
}

func (c *realNvidiaNVMLHardwareClient) CUDADriverVersion() (string, bool) {
	version, ret := c.api.SystemGetCudaDriverVersion_v2()
	if ret != nvml.SUCCESS {
		version, ret = c.api.SystemGetCudaDriverVersion()
	}

	if ret != nvml.SUCCESS {
		return "", false
	}

	return nvidiaFormatCUDADriverVersion(float64(version)), true
}

func (d realNvidiaNVMLHardwareDevice) UUID() (string, error) {
	value, ret := d.device.GetUUID()
	return value, nvidiaNVMLHardwareError("nvmlDeviceGetUUID", ret)
}

func nvidiaNVMLHardwareError(operation string, ret nvml.Return) error {
	if ret == nvml.SUCCESS {
		return nil
	}

	return fmt.Errorf("%s: %v (ret=%d)", operation, ret, ret)
}

func (d realNvidiaNVMLHardwareDevice) Index() (int, bool) {
	value, ret := d.device.GetIndex()
	return value, ret == nvml.SUCCESS
}

func (d realNvidiaNVMLHardwareDevice) MinorNumber() (int, bool) {
	value, ret := d.device.GetMinorNumber()
	return value, ret == nvml.SUCCESS
}

func (d realNvidiaNVMLHardwareDevice) Product() (string, bool) {
	value, ret := d.device.GetName()
	return value, ret == nvml.SUCCESS
}

func (d realNvidiaNVMLHardwareDevice) Architecture() (uint32, bool) {
	value, ret := d.device.GetArchitecture()
	return uint32(value), ret == nvml.SUCCESS
}

func (d realNvidiaNVMLHardwareDevice) CUDACapability() (int, int, bool) {
	major, minor, ret := d.device.GetCudaComputeCapability()
	return major, minor, ret == nvml.SUCCESS
}

func (d realNvidiaNVMLHardwareDevice) PCIEBusID() (string, bool) {
	value, ret := d.device.GetPciInfo()
	if ret != nvml.SUCCESS {
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
	return value, ret == nvml.SUCCESS
}

func (d realNvidiaNVMLHardwareDevice) PCIEWidth() (int, bool) {
	value, ret := d.device.GetMaxPcieLinkWidth()
	return value, ret == nvml.SUCCESS
}

func (d realNvidiaNVMLHardwareDevice) MemoryTotalBytes() (uint64, error) {
	value, ret := d.device.GetMemoryInfo()
	if ret == nvml.ERROR_NOT_SUPPORTED {
		return 0, nil
	}

	return value.Total, nvidiaNVMLHardwareError("nvmlDeviceGetMemoryInfo", ret)
}

func (d realNvidiaNVMLHardwareDevice) NUMANode() (int, bool) {
	value, ret := d.device.GetNumaNodeId()
	return value, ret == nvml.SUCCESS
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
