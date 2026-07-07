//go:build linux && cgo

package hardware

import "github.com/NVIDIA/go-nvml/pkg/nvml"

type realNVMLGPUHardwareClient struct {
	api nvml.Interface
}

type realNVMLGPUHardwareDevice struct {
	device nvml.Device
}

func newNVMLGPUHardwareClient() nvmlGPUHardwareClient {
	return &realNVMLGPUHardwareClient{api: nvml.New()}
}

func (c *realNVMLGPUHardwareClient) Init() bool {
	return c.api.Init() == nvml.SUCCESS
}

func (c *realNVMLGPUHardwareClient) Shutdown() {
	_ = c.api.Shutdown()
}

func (c *realNVMLGPUHardwareClient) DeviceCount() (int, bool) {
	count, ret := c.api.DeviceGetCount()
	return count, ret == nvml.SUCCESS
}

func (c *realNVMLGPUHardwareClient) Device(index int) (nvmlGPUHardwareDevice, bool) {
	device, ret := c.api.DeviceGetHandleByIndex(index)
	if ret != nvml.SUCCESS {
		return nil, false
	}

	return realNVMLGPUHardwareDevice{device: device}, true
}

func (c *realNVMLGPUHardwareClient) DriverVersion() (string, bool) {
	version, ret := c.api.SystemGetDriverVersion()
	return version, ret == nvml.SUCCESS
}

func (c *realNVMLGPUHardwareClient) CUDADriverVersion() (string, bool) {
	version, ret := c.api.SystemGetCudaDriverVersion_v2()
	if ret != nvml.SUCCESS {
		version, ret = c.api.SystemGetCudaDriverVersion()
	}

	if ret != nvml.SUCCESS {
		return "", false
	}

	return formatCUDADriverVersion(float64(version)), true
}

func (d realNVMLGPUHardwareDevice) UUID() (string, bool) {
	value, ret := d.device.GetUUID()
	return value, ret == nvml.SUCCESS
}

func (d realNVMLGPUHardwareDevice) Index() (int, bool) {
	value, ret := d.device.GetIndex()
	return value, ret == nvml.SUCCESS
}

func (d realNVMLGPUHardwareDevice) MinorNumber() (int, bool) {
	value, ret := d.device.GetMinorNumber()
	return value, ret == nvml.SUCCESS
}

func (d realNVMLGPUHardwareDevice) Product() (string, bool) {
	value, ret := d.device.GetName()
	return value, ret == nvml.SUCCESS
}

func (d realNVMLGPUHardwareDevice) Architecture() (uint32, bool) {
	value, ret := d.device.GetArchitecture()
	return uint32(value), ret == nvml.SUCCESS
}

func (d realNVMLGPUHardwareDevice) CUDACapability() (int, int, bool) {
	major, minor, ret := d.device.GetCudaComputeCapability()
	return major, minor, ret == nvml.SUCCESS
}

func (d realNVMLGPUHardwareDevice) PCIEBusID() (string, bool) {
	value, ret := d.device.GetPciInfo()
	if ret != nvml.SUCCESS {
		return "", false
	}

	busID := cStringFromInt8(value.BusId[:])
	if busID == "" {
		busID = cStringFromInt8(value.BusIdLegacy[:])
	}

	return busID, busID != ""
}

func (d realNVMLGPUHardwareDevice) PCIEGeneration() (int, bool) {
	value, ret := d.device.GetMaxPcieLinkGeneration()
	return value, ret == nvml.SUCCESS
}

func (d realNVMLGPUHardwareDevice) PCIEWidth() (int, bool) {
	value, ret := d.device.GetMaxPcieLinkWidth()
	return value, ret == nvml.SUCCESS
}

func (d realNVMLGPUHardwareDevice) MemoryTotalBytes() (uint64, bool) {
	value, ret := d.device.GetMemoryInfo()
	return value.Total, ret == nvml.SUCCESS
}

func (d realNVMLGPUHardwareDevice) NUMANode() (int, bool) {
	value, ret := d.device.GetNumaNodeId()
	return value, ret == nvml.SUCCESS
}

func cStringFromInt8(value []int8) string {
	bytes := make([]byte, 0, len(value))

	for _, char := range value {
		if char == 0 {
			break
		}

		bytes = append(bytes, byte(char))
	}

	return string(bytes)
}
