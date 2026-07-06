package normalizer

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	prommodel "github.com/prometheus/common/model"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/devicesnapshot"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hardware"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
)

const (
	TargetNodeExporter        = "node-exporter"
	TargetAcceleratorExporter = "accelerator-exporter"
)

type NormalizeRequest struct {
	Labels                       model.CanonicalLabels
	NodeExporter                 model.ScrapeResult
	AcceleratorExporter          *model.ScrapeResult
	EndpointAllocations          []model.EndpointAllocation
	GPUHardwareInfos             []model.GPUHardwareInfo
	EndpointReplicaRuntimeUsages []model.EndpointReplicaRuntimeUsage
	EndpointReplicaGPUUsages     []model.EndpointReplicaGPUUsage
}

type Normalizer struct{}

type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

func (n *Normalizer) Samples(req NormalizeRequest) []Sample {
	var samples []Sample

	samples = append(samples, nodeReadySample(req.Labels))
	samples = append(samples, scrapeUpSample(req.Labels, TargetNodeExporter, req.NodeExporter.Up))

	if req.NodeExporter.Up {
		samples = append(samples, normalizeNodeSamples(req.Labels, req.NodeExporter.Body)...)
	}

	if req.AcceleratorExporter != nil {
		samples = append(samples, scrapeUpSample(req.Labels, TargetAcceleratorExporter, req.AcceleratorExporter.Up))

		if req.AcceleratorExporter.Up {
			acceleratorIndexes := acceleratorIndexesByUUID(req.AcceleratorExporter.Body, req.GPUHardwareInfos)
			samples = append(samples, normalizeAcceleratorSamples(req.Labels, req.AcceleratorExporter.Body)...)
			samples = append(samples, normalizeNodeGPUSamples(
				req.Labels,
				req.AcceleratorExporter.Body,
				req.EndpointAllocations,
			)...)
			samples = append(samples, normalizeGPUHardwareInfoSamples(
				req.Labels,
				req.GPUHardwareInfos,
				req.AcceleratorExporter.Body,
			)...)
			samples = append(samples, normalizeEndpointAllocationSamples(
				req.Labels,
				req.EndpointAllocations,
				acceleratorIndexes,
				req.AcceleratorExporter.Body,
			)...)
		} else {
			samples = append(samples, normalizeEndpointAllocationSamples(req.Labels, req.EndpointAllocations, nil, "")...)
		}
	} else {
		samples = append(samples, normalizeEndpointAllocationSamples(req.Labels, req.EndpointAllocations, nil, "")...)
	}

	if req.AcceleratorExporter != nil && req.AcceleratorExporter.Up {
		samples = append(samples, normalizeEndpointReplicaGPUUsageFromDCGMSamples(
			req.Labels,
			req.AcceleratorExporter.Body,
			req.EndpointAllocations,
			req.EndpointReplicaGPUUsages,
		)...)
	}

	samples = append(samples, normalizeEndpointReplicaRuntimeUsageSamples(
		req.Labels,
		req.EndpointReplicaRuntimeUsages,
	)...)
	samples = append(samples, normalizeEndpointReplicaGPUUsageSamples(
		req.Labels,
		req.EndpointReplicaGPUUsages,
	)...)

	sort.SliceStable(samples, func(i, j int) bool {
		if samples[i].Name == samples[j].Name {
			return labelsKey(samples[i].Labels) < labelsKey(samples[j].Labels)
		}

		return samples[i].Name < samples[j].Name
	})

	return samples
}

func normalizeNodeSamples(labels model.CanonicalLabels, raw string) []Sample {
	samples := promtext.ParseVector(raw)
	parsed := indexFirstSampleByName(samples)
	var result []Sample

	for _, s := range samples {
		if promtext.MetricName(s) != "node_cpu_seconds_total" {
			continue
		}

		metricLabels := baseLabels(labels, TargetNodeExporter)
		if cpu := promtext.LabelValue(s, "cpu"); cpu != "" {
			metricLabels["cpu"] = cpu
		}

		if mode := promtext.LabelValue(s, "mode"); mode != "" {
			metricLabels["mode"] = mode
		}

		result = append(result, Sample{
			Name:   "neutree_node_cpu_seconds_total",
			Labels: metricLabels,
			Value:  promtext.Value(s),
		})
	}

	if total, ok := parsed["node_memory_MemTotal_bytes"]; ok {
		result = append(result, Sample{
			Name:   "neutree_node_memory_total_bytes",
			Labels: baseLabels(labels, TargetNodeExporter),
			Value:  promtext.Value(total),
		})
	}

	if available, ok := parsed["node_memory_MemAvailable_bytes"]; ok {
		result = append(result, Sample{
			Name:   "neutree_node_memory_available_bytes",
			Labels: baseLabels(labels, TargetNodeExporter),
			Value:  promtext.Value(available),
		})
	}

	total, hasTotal := parsed["node_memory_MemTotal_bytes"]
	available, hasAvailable := parsed["node_memory_MemAvailable_bytes"]

	if hasTotal && hasAvailable {
		result = append(result, Sample{
			Name:   "neutree_node_memory_used_bytes",
			Labels: baseLabels(labels, TargetNodeExporter),
			Value:  promtext.Value(total) - promtext.Value(available),
		})
	}

	if load1, ok := parsed["node_load1"]; ok {
		result = append(result, Sample{
			Name:   "neutree_node_load1",
			Labels: baseLabels(labels, TargetNodeExporter),
			Value:  promtext.Value(load1),
		})
	}

	return result
}

func normalizeAcceleratorSamples(labels model.CanonicalLabels, raw string) []Sample {
	parsed := promtext.ParseVector(raw)
	result := make([]Sample, 0)

	for _, s := range parsed {
		metricLabels, ok := acceleratorMetricLabels(labels, s)
		if !ok {
			continue
		}

		switch promtext.MetricName(s) {
		case "DCGM_FI_DEV_GPU_UTIL":
			value := promtext.Value(s)
			if value > 1 {
				value /= 100
			}

			result = append(result, Sample{
				Name:   "neutree_accelerator_utilization_ratio",
				Labels: metricLabels,
				Value:  value,
			})
		case "DCGM_FI_DEV_FB_USED":
			result = append(result, Sample{
				Name:   "neutree_accelerator_memory_used_bytes",
				Labels: metricLabels,
				Value:  promtext.Value(s) * 1024 * 1024,
			})
		case "DCGM_FI_DEV_FB_TOTAL":
			result = append(result, Sample{
				Name:   "neutree_accelerator_memory_total_bytes",
				Labels: metricLabels,
				Value:  promtext.Value(s) * 1024 * 1024,
			})
		case "DCGM_FI_DEV_GPU_TEMP":
			result = append(result, Sample{
				Name:   "neutree_accelerator_temperature_celsius",
				Labels: metricLabels,
				Value:  promtext.Value(s),
			})
		case "DCGM_FI_PROF_PCIE_TX_BYTES":
			result = append(result, Sample{
				Name:   "neutree_accelerator_pcie_tx_bytes_total",
				Labels: metricLabels,
				Value:  promtext.Value(s),
			})
		case "DCGM_FI_PROF_PCIE_RX_BYTES":
			result = append(result, Sample{
				Name:   "neutree_accelerator_pcie_rx_bytes_total",
				Labels: metricLabels,
				Value:  promtext.Value(s),
			})
		}
	}

	return result
}

func normalizeNodeGPUSamples(
	labels model.CanonicalLabels,
	raw string,
	allocations []model.EndpointAllocation,
) []Sample {
	devices := devicesnapshot.FromAcceleratorMetrics(raw).Accelerator.Devices
	if len(devices) == 0 {
		return nil
	}

	allocatedByUUID := allocatedDeviceUUIDs(allocations)
	totalByProduct := map[string]float64{}
	allocatedByProduct := map[string]float64{}
	result := make([]Sample, 0, len(devices)*2+len(totalByProduct)*3)

	for _, device := range devices {
		if device.UUID == "" {
			continue
		}

		product := firstNonEmpty(device.ProductModel, device.ProductName)
		totalByProduct[product]++

		if _, ok := allocatedByUUID[device.UUID]; ok {
			allocatedByProduct[product]++
		}

		metricLabels := physicalAcceleratorLabels(
			labels,
			v1.AcceleratorTypeNVIDIAGPU.String(),
			device.UUID,
			device.ID,
			product,
		)

		result = append(result, Sample{
			Name:   "neutree_node_accelerator_info",
			Labels: metricLabels,
			Value:  1,
		})
	}

	products := make([]string, 0, len(totalByProduct))
	for product := range totalByProduct {
		products = append(products, product)
	}

	sort.Strings(products)

	for _, product := range products {
		total := totalByProduct[product]
		allocated := allocatedByProduct[product]
		free := total - allocated
		metricLabels := nodeAcceleratorProductLabels(labels, v1.AcceleratorTypeNVIDIAGPU.String(), product)

		result = append(result,
			Sample{
				Name:   "neutree_node_accelerator_total",
				Labels: cloneLabels(metricLabels),
				Value:  total,
			},
			Sample{
				Name:   "neutree_node_accelerator_allocated",
				Labels: cloneLabels(metricLabels),
				Value:  allocated,
			},
			Sample{
				Name:   "neutree_node_accelerator_free",
				Labels: cloneLabels(metricLabels),
				Value:  free,
			},
		)
	}

	return result
}

func normalizeGPUHardwareInfoSamples(labels model.CanonicalLabels, infos []model.GPUHardwareInfo, raw string) []Sample {
	discoveredUUIDs := discoveredGPUUUIDs(raw)
	if len(discoveredUUIDs) == 0 {
		return nil
	}

	result := make([]Sample, 0, len(infos))

	for _, info := range infos {
		if info.UUID == "" {
			continue
		}

		if _, ok := discoveredUUIDs[info.UUID]; !ok {
			continue
		}

		commonLabels := physicalAcceleratorLabels(
			labels,
			v1.AcceleratorTypeNVIDIAGPU.String(),
			info.UUID,
			info.Index,
			info.Product,
		)
		commonLabels["memory_total_bytes"] = memoryMiBLabelToBytes(info.MemoryTotalMiB)
		commonLabels["pcie_bus_id"] = hardware.LabelValue(info.PCIEBusID)
		commonLabels["pcie_generation"] = hardware.LabelValue(info.PCIEGeneration)
		commonLabels["pcie_width"] = hardware.LabelValue(info.PCIEWidth)
		commonLabels["numa_node"] = hardware.LabelValue(info.NUMANode)

		nvidiaLabels := physicalAcceleratorLabels(
			labels,
			v1.AcceleratorTypeNVIDIAGPU.String(),
			info.UUID,
			info.Index,
			info.Product,
		)
		nvidiaLabels["architecture"] = hardware.LabelValue(info.Architecture)
		nvidiaLabels["cuda_capability"] = hardware.LabelValue(info.CUDACapability)
		nvidiaLabels["driver_version"] = hardware.LabelValue(info.DriverVersion)
		nvidiaLabels["cuda_driver_version"] = hardware.LabelValue(info.CUDADriverVersion)
		nvidiaLabels["nvlink"] = hardware.LabelValue(info.NVLink)
		nvidiaLabels["nvswitch"] = hardware.LabelValue(info.NVSwitch)

		result = append(result,
			Sample{
				Name:   "neutree_node_accelerator_hardware_info",
				Labels: commonLabels,
				Value:  1,
			},
			Sample{
				Name:   "neutree_node_accelerator_nvidia_info",
				Labels: nvidiaLabels,
				Value:  1,
			},
		)
	}

	return result
}

func discoveredGPUUUIDs(raw string) map[string]struct{} {
	result := map[string]struct{}{}

	for _, device := range devicesnapshot.FromAcceleratorMetrics(raw).Accelerator.Devices {
		if device.UUID == "" {
			continue
		}

		result[device.UUID] = struct{}{}
	}

	return result
}

func normalizeEndpointAllocationSamples(
	labels model.CanonicalLabels,
	allocations []model.EndpointAllocation,
	acceleratorIndexes map[string]string,
	acceleratorRaw string,
) []Sample {
	result := make([]Sample, 0)
	physicalVRAMs := physicalVRAMByUUID(acceleratorRaw)
	uniqueAllocations := uniqueEndpointAllocationsByGPUUUID(allocations)

	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID == "" {
				continue
			}

			physicalVRAM := physicalVRAMs[device.UUID]
			var derivedUsedBytes *float64
			if _, ok := uniqueAllocations[device.UUID]; ok && device.UsedMemoryMiB <= 0 && physicalVRAM.hasUsed {
				derivedUsedBytes = &physicalVRAM.usedBytes
			}
			metricLabels := endpointAllocationLabels(labels, allocation, device, acceleratorIndexes[device.UUID])

			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_accelerator_allocation",
				Labels: allocationInfoLabels(metricLabels, device, physicalVRAM, derivedUsedBytes),
				Value:  1,
			})
			if device.MemoryMiB > 0 {
				result = append(result, Sample{
					Name:   "neutree_endpoint_replica_accelerator_memory_allocated_bytes",
					Labels: metricLabels,
					Value:  mibToBytes(device.MemoryMiB),
				})
			}

			if device.UsedMemoryMiB > 0 {
				result = append(result, Sample{
					Name:   "neutree_endpoint_replica_accelerator_memory_used_bytes",
					Labels: metricLabels,
					Value:  mibToBytes(device.UsedMemoryMiB),
				})
			}
		}
	}

	return result
}

func endpointAllocationLabels(
	labels model.CanonicalLabels,
	allocation model.EndpointAllocation,
	device v1.DeviceAllocation,
	acceleratorIndex string,
) map[string]string {
	metricLabels := endpointAcceleratorLabels(
		labels,
		allocation.Endpoint,
		allocation.InstanceID,
		allocation.ReplicaID,
		firstNonEmpty(allocation.NodeID, device.NodeID, labels.Node),
		v1.AcceleratorTypeNVIDIAGPU.String(),
		device.UUID,
		acceleratorIndex,
		"",
		device.Product,
	)

	return metricLabels
}

type vramSnapshot struct {
	usedBytes  float64
	totalBytes float64
	hasUsed    bool
	hasTotal   bool
}

func allocationInfoLabels(
	base map[string]string,
	device v1.DeviceAllocation,
	physicalVRAM vramSnapshot,
	derivedUsedBytes *float64,
) map[string]string {
	labels := cloneLabels(base)
	labels["vram_usage"] = allocationVRAMLabel(device, physicalVRAM, derivedUsedBytes)
	labels["physical_vram_usage"] = vramLabel(physicalVRAM)

	return labels
}

func allocationVRAMLabel(device v1.DeviceAllocation, physicalVRAM vramSnapshot, derivedUsedBytes *float64) string {
	usedBytes := mibToBytes(device.UsedMemoryMiB)
	if usedBytes <= 0 {
		if derivedUsedBytes == nil {
			return "unknown"
		}
		usedBytes = *derivedUsedBytes
	}

	if usedBytes <= 0 {
		return "unknown"
	}

	allocatedBytes := mibToBytes(device.MemoryMiB)
	if allocatedBytes <= 0 {
		if !physicalVRAM.hasTotal {
			return "unknown"
		}
		allocatedBytes = physicalVRAM.totalBytes
	}

	return displayBytes(usedBytes) + " / " + displayBytes(allocatedBytes)
}

func vramLabel(snapshot vramSnapshot) string {
	if !snapshot.hasUsed || !snapshot.hasTotal {
		return "unknown"
	}

	return displayBytes(snapshot.usedBytes) + " / " + displayBytes(snapshot.totalBytes)
}

func physicalVRAMByUUID(raw string) map[string]vramSnapshot {
	result := map[string]vramSnapshot{}

	for _, s := range promtext.ParseVector(raw) {
		uuid := promtext.LabelValue(s, "UUID", "uuid")
		if uuid == "" {
			continue
		}

		snapshot := result[uuid]
		switch promtext.MetricName(s) {
		case "DCGM_FI_DEV_FB_USED":
			snapshot.usedBytes = promtext.Value(s) * 1024 * 1024
			snapshot.hasUsed = true
		case "DCGM_FI_DEV_FB_TOTAL":
			snapshot.totalBytes = promtext.Value(s) * 1024 * 1024
			snapshot.hasTotal = true
		}

		result[uuid] = snapshot
	}

	return result
}

type gpuUsageSnapshot struct {
	memoryUsedBytes  *float64
	utilizationRatio *float64
	acceleratorIndex string
}

func gpuUsageSnapshotByUUID(samples prommodel.Vector) map[string]gpuUsageSnapshot {
	result := map[string]gpuUsageSnapshot{}

	for _, s := range samples {
		uuid := promtext.LabelValue(s, "UUID", "uuid")
		if uuid == "" {
			continue
		}

		snapshot := result[uuid]
		if index := promtext.LabelValue(s, "gpu", "GPU_I_ID"); index != "" {
			snapshot.acceleratorIndex = index
		}

		switch promtext.MetricName(s) {
		case "DCGM_FI_DEV_FB_USED":
			usedBytes := promtext.Value(s) * 1024 * 1024
			snapshot.memoryUsedBytes = &usedBytes
		case "DCGM_FI_DEV_GPU_UTIL":
			utilization := promtext.Value(s)
			if utilization > 1 {
				utilization /= 100
			}

			snapshot.utilizationRatio = &utilization
		}

		result[uuid] = snapshot
	}

	return result
}

func acceleratorIndexesByUUID(raw string, infos []model.GPUHardwareInfo) map[string]string {
	result := map[string]string{}

	for _, info := range infos {
		if info.UUID == "" || info.Index == "" {
			continue
		}

		result[info.UUID] = info.Index
	}

	for _, sample := range promtext.ParseVector(raw) {
		uuid := promtext.LabelValue(sample, "UUID", "uuid")
		index := promtext.LabelValue(sample, "gpu", "GPU_I_ID")

		if uuid == "" || index == "" {
			continue
		}

		result[uuid] = index
	}

	return result
}

func explicitEndpointReplicaGPUUsageUUIDs(usages []model.EndpointReplicaGPUUsage) map[string]struct{} {
	result := map[string]struct{}{}

	for _, usage := range usages {
		if usage.GPUUUID == "" {
			continue
		}

		result[usage.GPUUUID] = struct{}{}
	}

	return result
}

type endpointDeviceAllocation struct {
	allocation model.EndpointAllocation
	device     v1.DeviceAllocation
}

func uniqueEndpointAllocationsByGPUUUID(
	allocations []model.EndpointAllocation,
) map[string]endpointDeviceAllocation {
	counts := map[string]int{}
	result := map[string]endpointDeviceAllocation{}

	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID == "" {
				continue
			}

			counts[device.UUID]++
			result[device.UUID] = endpointDeviceAllocation{
				allocation: allocation,
				device:     device,
			}
		}
	}

	for uuid, count := range counts {
		if count != 1 {
			delete(result, uuid)
		}
	}

	return result
}

func mibToBytes(value int64) float64 {
	return float64(value) * 1024 * 1024
}

func displayBytes(value float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	unitIndex := 0

	for value >= 1024 && unitIndex < len(units)-1 {
		value /= 1024
		unitIndex++
	}

	if math.Trunc(value) == value {
		return strconv.FormatInt(int64(value), 10) + " " + units[unitIndex]
	}

	formatted := strconv.FormatFloat(value, 'f', 1, 64)
	formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")

	return formatted + " " + units[unitIndex]
}

func mibStringToBytes(value string) float64 {
	mib, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}

	return mib * 1024 * 1024
}

func memoryMiBLabelToBytes(value string) string {
	bytes := mibStringToBytes(value)
	if bytes <= 0 {
		return "unknown"
	}

	return formatFloat(bytes)
}

func normalizeEndpointReplicaRuntimeUsageSamples(
	labels model.CanonicalLabels,
	usages []model.EndpointReplicaRuntimeUsage,
) []Sample {
	result := make([]Sample, 0, len(usages)*5)

	for _, usage := range usages {
		metricLabels := endpointReplicaRuntimeUsageLabels(labels, usage)
		result = append(result, Sample{
			Name:   "neutree_endpoint_replica_cpu_usage_seconds_total",
			Labels: metricLabels,
			Value:  usage.CPUUsageSeconds,
		})

		if usage.MemoryUsageBytes != nil {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_memory_usage_bytes",
				Labels: metricLabels,
				Value:  *usage.MemoryUsageBytes,
			})
		}

		if usage.MemoryWorkingSetBytes != nil {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_memory_working_set_bytes",
				Labels: metricLabels,
				Value:  *usage.MemoryWorkingSetBytes,
			})
		}

		if usage.CPULimitCores != nil {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_cpu_limit_cores",
				Labels: metricLabels,
				Value:  *usage.CPULimitCores,
			})
		}

		if usage.MemoryLimitBytes != nil {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_memory_limit_bytes",
				Labels: metricLabels,
				Value:  *usage.MemoryLimitBytes,
			})
		}
	}

	return result
}

func endpointReplicaRuntimeUsageLabels(
	labels model.CanonicalLabels,
	usage model.EndpointReplicaRuntimeUsage,
) map[string]string {
	metricLabels := baseLabels(labels, model.SourceNodeAgent)
	metricLabels["endpoint"] = usage.Endpoint
	metricLabels["instance_id"] = usage.InstanceID
	metricLabels["replica"] = usage.ReplicaID
	metricLabels["node"] = firstNonEmpty(usage.NodeID, labels.Node)
	metricLabels["workload_role"] = labelValueOrUnknown(usage.WorkloadRole)
	metricLabels["container"] = usage.Container
	metricLabels["container_id"] = usage.ContainerID
	metricLabels["engine"] = usage.Engine
	metricLabels["engine_version"] = usage.EngineVersion

	return metricLabels
}

func normalizeEndpointReplicaGPUUsageSamples(
	labels model.CanonicalLabels,
	usages []model.EndpointReplicaGPUUsage,
) []Sample {
	result := make([]Sample, 0, len(usages)*4)

	for _, usage := range usages {
		if usage.GPUUUID == "" {
			continue
		}

		metricLabels := endpointReplicaGPUUsageLabels(labels, usage)
		if usage.MemoryUsedBytes != nil {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_accelerator_memory_used_bytes",
				Labels: metricLabels,
				Value:  *usage.MemoryUsedBytes,
			})
		}

		if usage.UtilizationRatio != nil {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_accelerator_utilization_ratio",
				Labels: metricLabels,
				Value:  *usage.UtilizationRatio,
			})
		}
	}

	return result
}

func normalizeEndpointReplicaGPUUsageFromDCGMSamples(
	labels model.CanonicalLabels,
	raw string,
	allocations []model.EndpointAllocation,
	explicitUsages []model.EndpointReplicaGPUUsage,
) []Sample {
	snapshots := gpuUsageSnapshotByUUID(promtext.ParseVector(raw))
	if len(snapshots) == 0 || len(allocations) == 0 {
		return nil
	}

	explicitUsageUUIDs := explicitEndpointReplicaGPUUsageUUIDs(explicitUsages)
	uniqueAllocations := uniqueEndpointAllocationsByGPUUUID(allocations)
	result := make([]Sample, 0, len(uniqueAllocations)*2)

	for uuid, allocation := range uniqueAllocations {
		if _, ok := explicitUsageUUIDs[uuid]; ok {
			continue
		}

		snapshot := snapshots[uuid]
		if snapshot.memoryUsedBytes == nil && snapshot.utilizationRatio == nil {
			continue
		}

		metricLabels := endpointAllocationLabels(labels, allocation.allocation, allocation.device, snapshot.acceleratorIndex)
		if snapshot.memoryUsedBytes != nil && allocation.device.UsedMemoryMiB == 0 {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_accelerator_memory_used_bytes",
				Labels: metricLabels,
				Value:  *snapshot.memoryUsedBytes,
			})
		}

		if snapshot.utilizationRatio != nil {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_accelerator_utilization_ratio",
				Labels: metricLabels,
				Value:  *snapshot.utilizationRatio,
			})
		}
	}

	return result
}

func endpointReplicaGPUUsageLabels(
	labels model.CanonicalLabels,
	usage model.EndpointReplicaGPUUsage,
) map[string]string {
	return endpointAcceleratorLabels(
		labels,
		usage.Endpoint,
		usage.InstanceID,
		usage.ReplicaID,
		firstNonEmpty(usage.NodeID, labels.Node),
		firstNonEmpty(usage.AcceleratorType, v1.AcceleratorTypeNVIDIAGPU.String()),
		usage.GPUUUID,
		usage.AcceleratorIndex,
		usage.VDeviceIndex,
		usage.Product,
	)
}

func nodeReadySample(labels model.CanonicalLabels) Sample {
	metricLabels := baseLabels(labels, model.SourceNodeAgent)

	return Sample{
		Name:   "neutree_node_ready",
		Labels: metricLabels,
		Value:  1,
	}
}

func scrapeUpSample(labels model.CanonicalLabels, target string, up bool) Sample {
	value := float64(0)
	if up {
		value = 1
	}

	metricLabels := baseLabels(labels, model.SourceNodeAgent)
	metricLabels["target"] = target

	return Sample{
		Name:   "neutree_metrics_scrape_up",
		Labels: metricLabels,
		Value:  value,
	}
}

func physicalAcceleratorLabels(
	labels model.CanonicalLabels,
	acceleratorType string,
	uuid string,
	acceleratorIndex string,
	product string,
) map[string]string {
	metricLabels := acceleratorBaseLabels(labels)
	metricLabels["accelerator_type"] = labelValueOrUnknown(acceleratorType)
	metricLabels["accelerator_uuid"] = uuid
	metricLabels["accelerator_index"] = labelValueOrUnknown(acceleratorIndex)
	metricLabels["product"] = labelValueOrUnknown(product)

	return metricLabels
}

func nodeAcceleratorProductLabels(
	labels model.CanonicalLabels,
	acceleratorType string,
	product string,
) map[string]string {
	metricLabels := acceleratorBaseLabels(labels)
	metricLabels["accelerator_type"] = labelValueOrUnknown(acceleratorType)
	metricLabels["product"] = labelValueOrUnknown(product)

	return metricLabels
}

func endpointAcceleratorLabels(
	labels model.CanonicalLabels,
	endpoint string,
	instanceID string,
	replicaID string,
	node string,
	acceleratorType string,
	uuid string,
	acceleratorIndex string,
	vdeviceIndex string,
	product string,
) map[string]string {
	return map[string]string{
		"cluster_type":      labelValueOrUnknown(labels.ClusterType),
		"endpoint":          labelValueOrUnknown(endpoint),
		"instance_id":       labelValueOrUnknown(instanceID),
		"replica":           labelValueOrUnknown(replicaID),
		"node":              labelValueOrUnknown(firstNonEmpty(node, labels.Node)),
		"accelerator_type":  labelValueOrUnknown(acceleratorType),
		"accelerator_uuid":  uuid,
		"accelerator_index": labelValueOrUnknown(acceleratorIndex),
		"vdevice_index":     vdeviceIndexOrDefault(vdeviceIndex),
		"product":           labelValueOrUnknown(product),
	}
}

func acceleratorBaseLabels(labels model.CanonicalLabels) map[string]string {
	return map[string]string{
		"cluster_type": labelValueOrUnknown(labels.ClusterType),
		"node":         labelValueOrUnknown(labels.Node),
	}
}

func labelValueOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}

	return value
}

func vdeviceIndexOrDefault(value string) string {
	if value == "" {
		return "0"
	}

	return value
}

func allocatedDeviceUUIDs(allocations []model.EndpointAllocation) map[string]struct{} {
	result := map[string]struct{}{}

	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID == "" {
				continue
			}

			result[device.UUID] = struct{}{}
		}
	}

	return result
}

func cloneLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels))
	for key, value := range labels {
		result[key] = value
	}

	return result
}

func baseLabels(labels model.CanonicalLabels, source string) map[string]string {
	result := map[string]string{
		"source": source,
	}

	if labels.ClusterType != "" {
		result["cluster_type"] = labels.ClusterType
	}

	if labels.Node != "" {
		result["node"] = labels.Node
	}

	if labels.NodeIP != "" {
		result["node_ip"] = labels.NodeIP
	}

	if labels.NodeRole != "" {
		result["node_role"] = labels.NodeRole
	}

	return result
}

func acceleratorMetricLabels(labels model.CanonicalLabels, s *prommodel.Sample) (map[string]string, bool) {
	uuid := promtext.LabelValue(s, "UUID", "uuid")
	if uuid == "" {
		return nil, false
	}

	return physicalAcceleratorLabels(
		labels,
		v1.AcceleratorTypeNVIDIAGPU.String(),
		uuid,
		promtext.LabelValue(s, "gpu", "GPU_I_ID"),
		promtext.LabelValue(s, "modelName", "model"),
	), true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

func indexFirstSampleByName(samples prommodel.Vector) map[string]*prommodel.Sample {
	result := make(map[string]*prommodel.Sample, len(samples))

	for _, s := range samples {
		name := promtext.MetricName(s)
		if _, exists := result[name]; exists {
			continue
		}

		result[name] = s
	}

	return result
}

func formatSample(s Sample) string {
	return fmt.Sprintf("%s%s %s", s.Name, formatLabels(s.Labels), formatFloat(s.Value))
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	parts := make([]string, 0, len(labels))
	for _, key := range keys {
		parts = append(parts, key+`="`+escapeLabelValue(labels[key])+`"`)
	}

	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)

	return strings.ReplaceAll(value, `"`, `\"`)
}

func formatFloat(value float64) string {
	if math.Trunc(value) == value {
		return strconv.FormatInt(int64(value), 10)
	}

	return strconv.FormatFloat(value, 'f', -1, 64)
}

func labelsKey(labels map[string]string) string {
	return formatLabels(labels)
}
