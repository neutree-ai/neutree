package nvidia

type nvidiaHardwareInfo struct {
	UUID              string
	Index             string
	MinorNumber       string
	Product           string
	Architecture      string
	CUDACapability    string
	DriverVersion     string
	CUDADriverVersion string
	MemoryTotalMiB    string
	NVLink            string
	NVSwitch          string
	PCIEBusID         string
	PCIEGeneration    string
	PCIEWidth         string
	NUMANode          string
}
