package neutreemetrics

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	dcgmFBTotalMetric    = "DCGM_FI_DEV_FB_TOTAL"
	unknownHardwareValue = "unknown"
)

var (
	nvidiaSMIFullHardwareFields = []string{
		"index",
		"uuid",
		"name",
		"driver_version",
		"compute_cap",
		"pci.bus_id",
		"pcie.link.gen.max",
		"pcie.link.width.max",
		"memory.total",
	}
	nvidiaSMIMinimalHardwareFields = []string{
		"index",
		"uuid",
		"name",
		"driver_version",
		"pci.bus_id",
		"memory.total",
	}
)

type GPUHardwareInfo struct {
	UUID              string
	Index             string
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

type GPUHardwareInfoProvider interface {
	GPUHardwareInfos(ctx context.Context) ([]GPUHardwareInfo, error)
}

type GPUHardwareInfoProviderFunc func(ctx context.Context) ([]GPUHardwareInfo, error)

func (f GPUHardwareInfoProviderFunc) GPUHardwareInfos(ctx context.Context) ([]GPUHardwareInfo, error) {
	return f(ctx)
}

type NvidiaSMIGPUHardwareInfoProvider struct {
	Command   string
	SysFSRoot string
}

func (p NvidiaSMIGPUHardwareInfoProvider) GPUHardwareInfos(ctx context.Context) ([]GPUHardwareInfo, error) {
	command := p.Command
	if command == "" {
		command = "nvidia-smi"
	}

	infos, err := p.queryGPUHardwareInfos(ctx, command, nvidiaSMIFullHardwareFields)
	if err != nil {
		infos, err = p.queryGPUHardwareInfos(ctx, command, nvidiaSMIMinimalHardwareFields)
	}

	if err != nil {
		return nil, nil
	}

	cudaDriverVersion := p.cudaDriverVersion(ctx, command)
	architecturesByUUID := p.productArchitectures(ctx, command)

	for i := range infos {
		if infos[i].Architecture == "" {
			infos[i].Architecture = architecturesByUUID[infos[i].UUID]
		}

		if infos[i].CUDADriverVersion == "" {
			infos[i].CUDADriverVersion = cudaDriverVersion
		}

		if infos[i].NUMANode == "" {
			infos[i].NUMANode = numaNodeFromSysFS(p.SysFSRoot, infos[i].PCIEBusID)
		}
	}

	return infos, nil
}

func (p NvidiaSMIGPUHardwareInfoProvider) queryGPUHardwareInfos(
	ctx context.Context,
	command string,
	fields []string,
) ([]GPUHardwareInfo, error) {
	args := []string{
		"--query-gpu=" + strings.Join(fields, ","),
		"--format=csv,noheader,nounits",
	}

	out, err := exec.CommandContext(ctx, command, args...).Output()
	if err != nil {
		return nil, err
	}

	return parseNvidiaSMIGPUHardwareCSV(string(out), fields), nil
}

func (p NvidiaSMIGPUHardwareInfoProvider) cudaDriverVersion(ctx context.Context, command string) string {
	out, err := exec.CommandContext(ctx, command).Output()
	if err != nil {
		return ""
	}

	return parseNvidiaSMICUDAVersion(string(out))
}

func (p NvidiaSMIGPUHardwareInfoProvider) productArchitectures(ctx context.Context, command string) map[string]string {
	out, err := exec.CommandContext(ctx, command, "-q", "-x").Output()
	if err != nil {
		return nil
	}

	return parseNvidiaSMIProductArchitecturesXML(string(out))
}

type nvidiaSMIQueryXML struct {
	GPUs []nvidiaSMIQueryGPUXML `xml:"gpu"`
}

type nvidiaSMIQueryGPUXML struct {
	UUID                string `xml:"uuid"`
	ProductArchitecture string `xml:"product_architecture"`
}

func parseNvidiaSMIProductArchitecturesXML(raw string) map[string]string {
	var query nvidiaSMIQueryXML
	if err := xml.Unmarshal([]byte(raw), &query); err != nil {
		return nil
	}

	architecturesByUUID := make(map[string]string, len(query.GPUs))

	for _, gpu := range query.GPUs {
		uuid := cleanHardwareValue(gpu.UUID)
		architecture := cleanHardwareValue(gpu.ProductArchitecture)

		if uuid == "" || isUnknownHardwareLiteral(uuid) || isUnknownHardwareLiteral(architecture) {
			continue
		}

		architecturesByUUID[uuid] = architecture
	}

	return architecturesByUUID
}

func parseNvidiaSMIGPUHardwareCSV(raw string, fields []string) []GPUHardwareInfo {
	reader := csv.NewReader(bytes.NewBufferString(raw))
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil
	}

	infos := make([]GPUHardwareInfo, 0, len(records))

	for _, record := range records {
		info := GPUHardwareInfo{}

		for i, field := range fields {
			if i >= len(record) {
				continue
			}

			applyNvidiaSMIHardwareField(&info, field, record[i])
		}

		if info.UUID != "" {
			infos = append(infos, info)
		}
	}

	return infos
}

func applyNvidiaSMIHardwareField(info *GPUHardwareInfo, field, value string) {
	value = cleanHardwareValue(value)
	if isUnknownHardwareLiteral(value) {
		return
	}

	switch field {
	case "index":
		info.Index = value
	case "uuid":
		info.UUID = value
	case "name":
		info.Product = value
	case "driver_version":
		info.DriverVersion = value
	case "compute_cap":
		info.CUDACapability = value
	case "pci.bus_id":
		info.PCIEBusID = value
	case "pcie.link.gen.max":
		info.PCIEGeneration = value
	case "pcie.link.width.max":
		info.PCIEWidth = value
	case "memory.total":
		info.MemoryTotalMiB = value
	}
}

func parseNvidiaSMICUDAVersion(raw string) string {
	const marker = "CUDA Version:"

	index := strings.Index(raw, marker)
	if index < 0 {
		return ""
	}

	value := strings.TrimSpace(raw[index+len(marker):])
	if value == "" {
		return ""
	}

	return strings.Fields(value)[0]
}

func gpuHardwareInfosFromAcceleratorMetrics(raw string) []GPUHardwareInfo {
	samples := parsePrometheusText(raw)
	infosByUUID := map[string]GPUHardwareInfo{}

	for _, s := range samples {
		uuid := firstNonEmpty(s.labels["UUID"], s.labels["uuid"])
		if uuid == "" {
			continue
		}

		info := infosByUUID[uuid]
		info.UUID = uuid
		applyHardwareLabelHints(&info, s.labels)

		if gpuIndex := firstNonEmpty(s.labels["gpu"], s.labels["GPU_I_ID"]); gpuIndex != "" {
			info.Index = gpuIndex
		}

		if model := firstNonEmpty(s.labels["modelName"], s.labels["model"]); model != "" {
			info.Product = model
		}

		applyDCGMHardwareSample(&info, s)
		infosByUUID[uuid] = info
	}

	infos := make([]GPUHardwareInfo, 0, len(infosByUUID))
	for _, info := range infosByUUID {
		infos = append(infos, info)
	}

	return infos
}

func applyDCGMHardwareSample(info *GPUHardwareInfo, s sample) {
	switch s.name {
	case dcgmFBTotalMetric:
		info.MemoryTotalMiB = formatFloat(s.value)
	case "DCGM_FI_DRIVER_VERSION", "DCGM_FI_SYSTEM_DRIVER_VERSION":
		info.DriverVersion = firstKnownHardwareValue(
			info.DriverVersion,
			hardwareLabelValue(s.labels, "DCGM_FI_DRIVER_VERSION", "driver_version", "Driver_Version", "version"),
		)
	case "DCGM_FI_CUDA_DRIVER_VERSION":
		info.CUDADriverVersion = firstKnownHardwareValue(
			info.CUDADriverVersion,
			formatCUDADriverVersion(s.value),
		)
	case "DCGM_FI_DEV_CUDA_COMPUTE_CAPABILITY", "DCGM_FI_CUDA_GPU_COMPUTE_CAPABILITY":
		info.CUDACapability = firstKnownHardwareValue(
			info.CUDACapability,
			formatCUDAComputeCapability(s.value),
		)
	case "DCGM_FI_DEV_PCI_BUSID", "DCGM_FI_DEV_PCI_BUS_ID", "DCGM_FI_DEV_PCIE_BUS_ID":
		info.PCIEBusID = firstKnownHardwareValue(
			info.PCIEBusID,
			hardwareLabelValue(s.labels, "DCGM_FI_DEV_PCI_BUSID", "pci_bus_id", "pcie_bus_id", "pci_busid", "bus_id"),
		)
	case "DCGM_FI_DEV_PCIE_MAX_LINK_GEN":
		info.PCIEGeneration = firstKnownHardwareValue(formatFloat(s.value), info.PCIEGeneration)
	case "DCGM_FI_DEV_PCIE_LINK_GEN":
		info.PCIEGeneration = firstKnownHardwareValue(info.PCIEGeneration, formatFloat(s.value))
	case "DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH":
		info.PCIEWidth = firstKnownHardwareValue(formatFloat(s.value), info.PCIEWidth)
	case "DCGM_FI_DEV_PCIE_LINK_WIDTH":
		info.PCIEWidth = firstKnownHardwareValue(info.PCIEWidth, formatFloat(s.value))
	case "DCGM_FI_DEV_P2P_NVLINK_STATUS", "DCGM_FI_DEV_NVLINK_P2P_STATUS", "DCGM_FI_SYSTEM_NVLINK_TOPOLOGY":
		if s.value > 0 {
			info.NVLink = "present"
		}
	case "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL":
		if s.value > 0 {
			info.NVLink = "present"
		}
	}
}

func applyHardwareLabelHints(info *GPUHardwareInfo, labels map[string]string) {
	info.Product = firstKnownHardwareValue(
		info.Product,
		hardwareLabelValue(
			labels,
			"DCGM_FI_DEV_NAME",
			"DCGM_FI_DEV_BRAND",
			"gpu_name",
			"GPU_Name",
			"device_name",
			"modelName",
			"model",
			"name",
		),
	)
	info.Architecture = firstKnownHardwareValue(
		info.Architecture,
		hardwareLabelValue(labels, "architecture", "gpu_architecture", "arch"),
	)
	info.CUDACapability = firstKnownHardwareValue(
		info.CUDACapability,
		hardwareLabelValue(labels, "cuda_capability", "cuda_compute_capability", "compute_capability", "compute_cap"),
	)
	info.DriverVersion = firstKnownHardwareValue(
		info.DriverVersion,
		hardwareLabelValue(labels, "DCGM_FI_DRIVER_VERSION", "driver_version", "Driver_Version", "driver"),
	)
	info.CUDADriverVersion = firstKnownHardwareValue(
		info.CUDADriverVersion,
		hardwareLabelValue(labels, "cuda_driver_version", "cuda_version", "CUDA_Version"),
	)
	info.PCIEBusID = firstKnownHardwareValue(
		info.PCIEBusID,
		hardwareLabelValue(labels, "DCGM_FI_DEV_PCI_BUSID", "pci_bus_id", "pcie_bus_id", "pci_busid", "bus_id"),
	)
	info.PCIEGeneration = firstKnownHardwareValue(
		info.PCIEGeneration,
		hardwareLabelValue(labels, "pcie_max_generation", "pcie_max_link_gen", "pcie_generation", "pcie_link_gen", "pcie_gen"),
	)
	info.PCIEWidth = firstKnownHardwareValue(
		info.PCIEWidth,
		hardwareLabelValue(labels, "pcie_max_width", "pcie_max_link_width", "pcie_width", "pcie_link_width"),
	)
	info.NUMANode = firstKnownHardwareValue(info.NUMANode, hardwareLabelValue(labels, "numa_node", "numa"))
	info.NVLink = firstKnownHardwareValue(info.NVLink, hardwareLabelValue(labels, "nvlink"))
	info.NVSwitch = firstKnownHardwareValue(info.NVSwitch, hardwareLabelValue(labels, "nvswitch", "nv_switch"))
}

func mergeGPUHardwareInfos(primary, fallback []GPUHardwareInfo) []GPUHardwareInfo {
	merged := map[string]GPUHardwareInfo{}
	order := make([]string, 0, len(primary)+len(fallback))

	for _, info := range primary {
		if info.UUID == "" {
			continue
		}

		merged[info.UUID] = info
		order = append(order, info.UUID)
	}

	for _, info := range fallback {
		if info.UUID == "" {
			continue
		}

		existing, ok := merged[info.UUID]
		if !ok {
			merged[info.UUID] = info
			order = append(order, info.UUID)

			continue
		}

		merged[info.UUID] = fillGPUHardwareInfo(existing, info)
	}

	result := make([]GPUHardwareInfo, 0, len(order))

	seen := map[string]struct{}{}
	for _, uuid := range order {
		if _, ok := seen[uuid]; ok {
			continue
		}

		seen[uuid] = struct{}{}

		result = append(result, merged[uuid])
	}

	return result
}

func fillGPUHardwareInfo(primary, fallback GPUHardwareInfo) GPUHardwareInfo {
	primary.Index = firstKnownHardwareValue(primary.Index, fallback.Index)
	primary.Product = firstKnownHardwareValue(primary.Product, fallback.Product)
	primary.Architecture = firstKnownHardwareValue(primary.Architecture, fallback.Architecture)
	primary.CUDACapability = firstKnownHardwareValue(primary.CUDACapability, fallback.CUDACapability)
	primary.DriverVersion = firstKnownHardwareValue(primary.DriverVersion, fallback.DriverVersion)
	primary.CUDADriverVersion = firstKnownHardwareValue(primary.CUDADriverVersion, fallback.CUDADriverVersion)
	primary.MemoryTotalMiB = firstKnownHardwareValue(primary.MemoryTotalMiB, fallback.MemoryTotalMiB)
	primary.NVLink = firstKnownHardwareValue(primary.NVLink, fallback.NVLink)
	primary.NVSwitch = firstKnownHardwareValue(primary.NVSwitch, fallback.NVSwitch)
	primary.PCIEBusID = firstKnownHardwareValue(primary.PCIEBusID, fallback.PCIEBusID)
	primary.PCIEGeneration = maxKnownHardwareNumber(primary.PCIEGeneration, fallback.PCIEGeneration)
	primary.PCIEWidth = maxKnownHardwareNumber(primary.PCIEWidth, fallback.PCIEWidth)
	primary.NUMANode = firstKnownHardwareValue(primary.NUMANode, fallback.NUMANode)

	return primary
}

func hardwareInfoLabelValue(value string) string {
	value = cleanHardwareValue(value)
	if isUnknownHardwareLiteral(value) {
		return unknownHardwareValue
	}

	return value
}

func firstKnownHardwareValue(values ...string) string {
	for _, value := range values {
		value = cleanHardwareValue(value)
		if !isUnknownHardwareLiteral(value) {
			return value
		}
	}

	return ""
}

func maxKnownHardwareNumber(values ...string) string {
	maxValue := ""
	maxNumber := 0.0

	for _, value := range values {
		value = cleanHardwareValue(value)
		if isUnknownHardwareLiteral(value) {
			continue
		}

		number, err := parseHardwareNumber(value)
		if err != nil {
			return firstKnownHardwareValue(values...)
		}

		if maxValue == "" || number > maxNumber {
			maxValue = value
			maxNumber = number
		}
	}

	return maxValue
}

func parseHardwareNumber(value string) (float64, error) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty hardware number")
	}

	return strconv.ParseFloat(fields[0], 64)
}

func hardwareLabelValue(labels map[string]string, keys ...string) string {
	normalized := make(map[string]string, len(labels))
	for key, value := range labels {
		normalized[normalizeHardwareLabelKey(key)] = value
	}

	for _, key := range keys {
		if value := cleanHardwareValue(normalized[normalizeHardwareLabelKey(key)]); !isUnknownHardwareLiteral(value) {
			return value
		}
	}

	return ""
}

func normalizeHardwareLabelKey(key string) string {
	key = strings.ToLower(key)
	replacer := strings.NewReplacer("_", "", "-", "", ".", "", " ", "")

	return replacer.Replace(key)
}

func cleanHardwareValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, " MiB")
	value = strings.TrimSuffix(value, " MiB/s")
	value = strings.TrimSuffix(value, " MB")
	value = strings.TrimSuffix(value, " MB/s")

	return strings.TrimSpace(value)
}

func isUnknownHardwareLiteral(value string) bool {
	value = strings.TrimSpace(value)

	return value == "" ||
		strings.EqualFold(value, unknownHardwareValue) ||
		strings.EqualFold(value, "N/A") ||
		strings.EqualFold(value, "[Not Supported]")
}

func formatCUDADriverVersion(value float64) string {
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

func formatCUDAComputeCapability(value float64) string {
	raw := int64(value)
	if raw <= 0 {
		return ""
	}

	if raw > 1<<32 {
		major := raw >> 32
		minor := raw & 0xffffffff

		return fmt.Sprintf("%d.%d", major, minor)
	}

	if raw > 1<<16 {
		major := (raw >> 16) & 0xffff
		minor := raw & 0xffff

		if major > 0 {
			return fmt.Sprintf("%d.%d", major, minor)
		}
	}

	if raw >= 10 && raw < 100 {
		return fmt.Sprintf("%d.%d", raw/10, raw%10)
	}

	return formatFloat(value)
}

func numaNodeFromSysFS(root, pciBusID string) string {
	deviceID := sysFSPCIDeviceID(pciBusID)
	if deviceID == "" {
		return ""
	}

	if root == "" {
		root = "/sys"
	}

	raw, err := os.ReadFile(filepath.Join(root, "bus", "pci", "devices", deviceID, "numa_node"))
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(raw))
}

func sysFSPCIDeviceID(pciBusID string) string {
	pciBusID = strings.TrimSpace(strings.ToLower(pciBusID))
	if pciBusID == "" {
		return ""
	}

	parts := strings.Split(pciBusID, ":")
	switch len(parts) {
	case 2:
		return "0000:" + pciBusID
	case 3:
		domain := parts[0]
		if len(domain) > 4 {
			domain = domain[len(domain)-4:]
		}

		return domain + ":" + parts[1] + ":" + parts[2]
	default:
		return ""
	}
}
