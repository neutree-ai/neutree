package hardware

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	prommodel "github.com/prometheus/common/model"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
)

const unknownHardwareValue = "unknown"
const hardwareAbsentValue = "0"
const hardwarePresentValue = "1"

type GPUHardwareInfoProvider interface {
	GPUHardwareInfos(ctx context.Context) ([]model.GPUHardwareInfo, error)
}

type GPUHardwareInfoProviderFunc func(ctx context.Context) ([]model.GPUHardwareInfo, error)

func (f GPUHardwareInfoProviderFunc) GPUHardwareInfos(ctx context.Context) ([]model.GPUHardwareInfo, error) {
	return f(ctx)
}

func FromAcceleratorMetrics(raw string) []model.GPUHardwareInfo {
	samples := promtext.ParseVector(raw)
	infosByUUID := map[string]model.GPUHardwareInfo{}

	for _, s := range samples {
		uuid := promtext.LabelValue(s, "UUID", "uuid", "gpu_uuid")
		if uuid == "" {
			continue
		}

		info := infosByUUID[uuid]
		info.UUID = uuid
		applyHardwareLabelHints(&info, promtext.Labels(s))

		if gpuIndex := promtext.LabelValue(s, "gpu", "GPU_I_ID", "DCGM_FI_DEV_NVML_INDEX", "nvml_index"); gpuIndex != "" {
			info.Index = gpuIndex
		}

		if model := promtext.LabelValue(s, "modelName", "model"); model != "" {
			info.Product = model
		}

		applyDCGMHardwareSample(&info, s)
		infosByUUID[uuid] = info
	}

	if nvswitch, ok := nvswitchPresenceFromLinkStatus(samples); ok {
		for uuid, info := range infosByUUID {
			info.NVSwitch = firstKnownHardwareValue(info.NVSwitch, nvswitch)
			infosByUUID[uuid] = info
		}
	}

	infos := make([]model.GPUHardwareInfo, 0, len(infosByUUID))
	for _, info := range infosByUUID {
		infos = append(infos, info)
	}

	return infos
}

func nvswitchPresenceFromLinkStatus(samples prommodel.Vector) (string, bool) {
	result := ""

	for _, s := range samples {
		if promtext.MetricName(s) != "DCGM_FI_DEV_NVSWITCH_LINK_STATUS" {
			continue
		}

		if promtext.Value(s) != 0 {
			return hardwarePresentValue, true
		}

		result = hardwareAbsentValue
	}

	return result, result != ""
}

func gpuHardwareInfosFromAcceleratorMetrics(raw string) []model.GPUHardwareInfo {
	return FromAcceleratorMetrics(raw)
}

func applyDCGMHardwareSample(info *model.GPUHardwareInfo, s *prommodel.Sample) {
	value := promtext.Value(s)
	labels := promtext.Labels(s)

	switch promtext.MetricName(s) {
	case "DCGM_FI_DEV_FB_TOTAL":
		info.MemoryTotalMiB = formatFloat(value)
	case "DCGM_FI_DEV_NVML_INDEX":
		info.Index = firstKnownHardwareValue(info.Index, formatFloat(value))
	case "DCGM_FI_DRIVER_VERSION", "DCGM_FI_SYSTEM_DRIVER_VERSION":
		info.DriverVersion = firstKnownHardwareValue(
			info.DriverVersion,
			hardwareLabelValue(
				labels,
				"DCGM_FI_DRIVER_VERSION",
				"DCGM_FI_SYSTEM_DRIVER_VERSION",
				"driver_version",
				"Driver_Version",
				"version",
			),
		)
	case "DCGM_FI_CUDA_DRIVER_VERSION":
		info.CUDADriverVersion = firstKnownHardwareValue(
			info.CUDADriverVersion,
			formatCUDADriverVersion(value),
		)
	case "DCGM_FI_DEV_CUDA_COMPUTE_CAPABILITY", "DCGM_FI_CUDA_GPU_COMPUTE_CAPABILITY":
		info.CUDACapability = firstKnownHardwareValue(
			info.CUDACapability,
			formatCUDAComputeCapability(value),
		)
	case "DCGM_FI_DEV_PCI_BUSID", "DCGM_FI_DEV_PCIE_BUS_ID":
		info.PCIEBusID = firstKnownHardwareValue(
			info.PCIEBusID,
			hardwareLabelValue(
				labels,
				"DCGM_FI_DEV_PCI_BUSID",
				"pci_bus_id",
				"pcie_bus_id",
				"pci_busid",
				"bus_id",
			),
		)
	case "DCGM_FI_DEV_PCIE_MAX_LINK_GEN":
		info.PCIEGeneration = firstKnownHardwareValue(formatFloat(value), info.PCIEGeneration)
	case "DCGM_FI_DEV_PCIE_LINK_GEN":
		info.PCIEGeneration = firstKnownHardwareValue(info.PCIEGeneration, formatFloat(value))
	case "DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH":
		info.PCIEWidth = firstKnownHardwareValue(formatFloat(value), info.PCIEWidth)
	case "DCGM_FI_DEV_PCIE_LINK_WIDTH":
		info.PCIEWidth = firstKnownHardwareValue(info.PCIEWidth, formatFloat(value))
	case "DCGM_FI_DEV_P2P_NVLINK_STATUS", "DCGM_FI_DEV_NVLINK_P2P_STATUS", "DCGM_FI_SYSTEM_NVLINK_TOPOLOGY":
		if value > 0 {
			info.NVLink = hardwarePresentValue
		}
	case "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL":
		if value > 0 {
			info.NVLink = hardwarePresentValue
		}
	case "DCGM_FI_DEV_NVSWITCH_LINK_STATUS":
		if value >= 0 {
			info.NVSwitch = hardwarePresentValue
		}
	}
}

func applyHardwareLabelHints(info *model.GPUHardwareInfo, labels map[string]string) {
	info.Product = firstKnownHardwareValue(
		info.Product,
		hardwareLabelValue(
			labels,
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
		hardwareLabelValue(
			labels,
			"DCGM_FI_DRIVER_VERSION",
			"DCGM_FI_SYSTEM_DRIVER_VERSION",
			"driver_version",
			"Driver_Version",
			"driver",
		),
	)
	info.CUDADriverVersion = firstKnownHardwareValue(
		info.CUDADriverVersion,
		hardwareLabelValue(labels, "cuda_driver_version", "cuda_version", "CUDA_Version"),
	)
	info.PCIEBusID = firstKnownHardwareValue(
		info.PCIEBusID,
		hardwareLabelValue(
			labels,
			"DCGM_FI_DEV_PCI_BUSID",
			"pci_bus_id",
			"pcie_bus_id",
			"pci_busid",
			"bus_id",
		),
	)
	info.PCIEGeneration = firstKnownHardwareValue(
		info.PCIEGeneration,
		hardwareLabelValue(labels, "pcie_max_generation", "pcie_max_link_gen", "pcie_generation", "pcie_link_gen", "pcie_gen"),
	)
	info.PCIEWidth = firstKnownHardwareValue(
		info.PCIEWidth,
		hardwareLabelValue(labels, "pcie_max_width", "pcie_max_link_width", "pcie_width", "pcie_link_width"),
	)
	info.Index = firstKnownHardwareValue(
		info.Index,
		hardwareLabelValue(labels, "DCGM_FI_DEV_NVML_INDEX", "nvml_index"),
	)
	info.MinorNumber = firstKnownHardwareValue(
		info.MinorNumber,
		hardwareLabelValue(labels, "gpu_minor_number", "minor_number"),
	)
	info.NUMANode = firstKnownHardwareValue(info.NUMANode, hardwareLabelValue(labels, "numa_node", "numa"))
	info.NVLink = firstKnownHardwareValue(info.NVLink, hardwareLabelValue(labels, "nvlink"))
	info.NVSwitch = firstKnownHardwareValue(info.NVSwitch, hardwareLabelValue(labels, "nvswitch", "nv_switch"))
}

func Merge(primary, fallback []model.GPUHardwareInfo) []model.GPUHardwareInfo {
	merged := map[string]model.GPUHardwareInfo{}
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

	result := make([]model.GPUHardwareInfo, 0, len(order))
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

func mergeGPUHardwareInfos(primary, fallback []model.GPUHardwareInfo) []model.GPUHardwareInfo {
	return Merge(primary, fallback)
}

func fillGPUHardwareInfo(primary, fallback model.GPUHardwareInfo) model.GPUHardwareInfo {
	primary.Index = firstKnownHardwareValue(primary.Index, fallback.Index)
	primary.MinorNumber = firstKnownHardwareValue(primary.MinorNumber, fallback.MinorNumber)
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

func LabelValue(value string) string {
	value = cleanHardwareValue(value)
	if isUnknownHardwareLiteral(value) {
		return unknownHardwareValue
	}

	return value
}

func PresenceLabelValue(value string) string {
	value = strings.ToLower(cleanHardwareValue(value))
	if value == "" || isUnknownHardwareLiteral(value) {
		return unknownHardwareValue
	}

	switch value {
	case "0", "false", "no", "none", "absent":
		return hardwareAbsentValue
	default:
		return hardwarePresentValue
	}
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

func formatFloat(value float64) string {
	if value == float64(int64(value)) {
		return strconv.FormatInt(int64(value), 10)
	}

	return strconv.FormatFloat(value, 'f', -1, 64)
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
