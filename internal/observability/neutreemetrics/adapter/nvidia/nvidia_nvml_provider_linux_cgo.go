//go:build linux && cgo

package nvidia

import "github.com/NVIDIA/go-nvml/pkg/nvml"

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
	return c.api.Init() == nvml.SUCCESS
}

func (c *realNvidiaNVMLHardwareClient) Shutdown() {
	_ = c.api.Shutdown()
}

func (c *realNvidiaNVMLHardwareClient) DeviceCount() (int, bool) {
	count, ret := c.api.DeviceGetCount()
	return count, ret == nvml.SUCCESS
}

func (c *realNvidiaNVMLHardwareClient) Device(index int) (nvidiaNVMLHardwareDevice, bool) {
	device, ret := c.api.DeviceGetHandleByIndex(index)
	if ret != nvml.SUCCESS {
		return nil, false
	}

	return realNvidiaNVMLHardwareDevice{device: device}, true
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

func (d realNvidiaNVMLHardwareDevice) UUID() (string, bool) {
	value, ret := d.device.GetUUID()
	return value, ret == nvml.SUCCESS
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

func (d realNvidiaNVMLHardwareDevice) MemoryTotalBytes() (uint64, bool) {
	value, ret := d.device.GetMemoryInfo()
	return value.Total, ret == nvml.SUCCESS
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
