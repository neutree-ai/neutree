package neutreemetrics

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	TargetNodeExporter        = "node-exporter"
	TargetAcceleratorExporter = "accelerator-exporter"
	TargetRuntimeUsage        = "runtime-usage"
)

type CanonicalLabels struct {
	Workspace         string
	NeutreeCluster    string
	StaticNodeCluster string
	ClusterType       string
	Node              string
	NodeIP            string
	NodeRole          string
}

type ScrapeResult struct {
	Target string
	Up     bool
	Body   string
	Error  string
}

type NormalizeRequest struct {
	Labels                       CanonicalLabels
	NodeExporter                 ScrapeResult
	AcceleratorExporter          *ScrapeResult
	RuntimeUsage                 *ScrapeResult
	EndpointAllocations          []EndpointAllocation
	GPUHardwareInfos             []GPUHardwareInfo
	EndpointReplicaRuntimeUsages []EndpointReplicaRuntimeUsage
}

type EndpointAllocation struct {
	Workspace  string
	Cluster    string
	Endpoint   string
	InstanceID string
	ReplicaID  string
	NodeID     string
	Devices    []v1.DeviceAllocation
}

type EndpointReplicaRuntimeUsage struct {
	Workspace             string
	Cluster               string
	Endpoint              string
	InstanceID            string
	ReplicaID             string
	NodeID                string
	Deployment            string
	Container             string
	ContainerID           string
	Engine                string
	EngineVersion         string
	CPUUsageSeconds       float64
	MemoryUsageBytes      *float64
	MemoryWorkingSetBytes *float64
	CPULimitCores         *float64
	MemoryLimitBytes      *float64
}

type Normalizer struct{}

type sample struct {
	name   string
	labels map[string]string
	value  float64
}

type canonicalSample struct {
	name   string
	labels map[string]string
	value  float64
}

func (n *Normalizer) Normalize(req NormalizeRequest) string {
	var samples []canonicalSample

	samples = append(samples, nodeReadySample(req.Labels))
	samples = append(samples, scrapeUpSample(req.Labels, TargetNodeExporter, req.NodeExporter.Up))

	if req.NodeExporter.Up {
		samples = append(samples, normalizeNodeSamples(req.Labels, req.NodeExporter.Body)...)
	}

	if req.AcceleratorExporter != nil {
		samples = append(samples, scrapeUpSample(req.Labels, TargetAcceleratorExporter, req.AcceleratorExporter.Up))
		if req.AcceleratorExporter.Up {
			samples = append(samples, normalizeAcceleratorSamples(req.Labels, req.AcceleratorExporter.Body)...)
			samples = append(samples, normalizeNodeGPUSamples(
				req.Labels,
				req.AcceleratorExporter.Body,
				req.EndpointAllocations,
			)...)
			samples = append(samples, normalizeGPUHardwareInfoSamples(req.Labels, req.GPUHardwareInfos)...)
		}
	}

	samples = append(samples, normalizeEndpointAllocationSamples(req.Labels, req.EndpointAllocations)...)
	if req.AcceleratorExporter != nil && req.AcceleratorExporter.Up {
		samples = append(samples, normalizeNodeGPUAllocationInfoSamples(
			req.Labels,
			req.AcceleratorExporter.Body,
			req.EndpointAllocations,
			req.GPUHardwareInfos,
		)...)
	}

	samples = append(samples, normalizeEndpointReplicaRuntimeUsageSamples(
		req.Labels,
		req.EndpointReplicaRuntimeUsages,
	)...)
	if req.RuntimeUsage != nil {
		samples = append(samples, scrapeUpSample(req.Labels, TargetRuntimeUsage, req.RuntimeUsage.Up))
	}

	sort.SliceStable(samples, func(i, j int) bool {
		if samples[i].name == samples[j].name {
			return labelsKey(samples[i].labels) < labelsKey(samples[j].labels)
		}

		return samples[i].name < samples[j].name
	})

	var builder strings.Builder
	for _, s := range samples {
		builder.WriteString(formatSample(s))
		builder.WriteByte('\n')
	}

	return builder.String()
}

func normalizeNodeSamples(labels CanonicalLabels, raw string) []canonicalSample {
	samples := parsePrometheusText(raw)
	parsed := indexFirstSampleByName(samples)
	var result []canonicalSample

	for _, s := range samples {
		if s.name != "node_cpu_seconds_total" {
			continue
		}

		metricLabels := baseLabels(labels, TargetNodeExporter)
		if cpu := s.labels["cpu"]; cpu != "" {
			metricLabels["cpu"] = cpu
		}

		if mode := s.labels["mode"]; mode != "" {
			metricLabels["mode"] = mode
		}

		result = append(result, canonicalSample{
			name:   "neutree_node_cpu_seconds_total",
			labels: metricLabels,
			value:  s.value,
		})
	}

	if total, ok := parsed["node_memory_MemTotal_bytes"]; ok {
		result = append(result, canonicalSample{
			name:   "neutree_node_memory_total_bytes",
			labels: baseLabels(labels, TargetNodeExporter),
			value:  total.value,
		})
	}

	if available, ok := parsed["node_memory_MemAvailable_bytes"]; ok {
		result = append(result, canonicalSample{
			name:   "neutree_node_memory_available_bytes",
			labels: baseLabels(labels, TargetNodeExporter),
			value:  available.value,
		})
	}

	total, hasTotal := parsed["node_memory_MemTotal_bytes"]
	available, hasAvailable := parsed["node_memory_MemAvailable_bytes"]

	if hasTotal && hasAvailable {
		result = append(result, canonicalSample{
			name:   "neutree_node_memory_used_bytes",
			labels: baseLabels(labels, TargetNodeExporter),
			value:  total.value - available.value,
		})
	}

	if load1, ok := parsed["node_load1"]; ok {
		result = append(result, canonicalSample{
			name:   "neutree_node_load1",
			labels: baseLabels(labels, TargetNodeExporter),
			value:  load1.value,
		})
	}

	return result
}

func normalizeAcceleratorSamples(labels CanonicalLabels, raw string) []canonicalSample {
	parsed := parsePrometheusText(raw)
	result := make([]canonicalSample, 0)

	for _, s := range parsed {
		metricLabels, ok := acceleratorMetricLabels(labels, s)
		if !ok {
			continue
		}

		switch s.name {
		case "DCGM_FI_DEV_GPU_UTIL":
			value := s.value
			if value > 1 {
				value /= 100
			}

			result = append(result, canonicalSample{
				name:   "neutree_gpu_utilization_ratio",
				labels: metricLabels,
				value:  value,
			})
		case "DCGM_FI_DEV_FB_USED":
			result = append(result, canonicalSample{
				name:   "neutree_gpu_memory_used_bytes",
				labels: metricLabels,
				value:  s.value * 1024 * 1024,
			})
		case dcgmFBTotalMetric:
			result = append(result, canonicalSample{
				name:   "neutree_gpu_memory_total_bytes",
				labels: metricLabels,
				value:  s.value * 1024 * 1024,
			})
		case "DCGM_FI_DEV_GPU_TEMP":
			result = append(result, canonicalSample{
				name:   "neutree_gpu_temperature_celsius",
				labels: metricLabels,
				value:  s.value,
			})
		}
	}

	return result
}

func normalizeNodeGPUSamples(
	labels CanonicalLabels,
	raw string,
	allocations []EndpointAllocation,
) []canonicalSample {
	devices := acceleratorDevicesFromMetrics(raw)
	if len(devices) == 0 {
		return nil
	}

	allocatedByUUID := allocatedDeviceUUIDs(allocations)
	totalByProduct := map[string]float64{}
	allocatedByProduct := map[string]float64{}
	result := make([]canonicalSample, 0, len(devices)*2+len(totalByProduct)*3)

	for _, device := range devices {
		if device.UUID == "" {
			continue
		}

		product := firstNonEmpty(device.ProductModel, device.ProductName, v1.AcceleratorTypeNVIDIAGPU.String())
		totalByProduct[product]++

		if _, ok := allocatedByUUID[device.UUID]; ok {
			allocatedByProduct[product]++
		}

		metricLabels := nodeGPULabels(labels, product)
		metricLabels["gpu_uuid"] = device.UUID

		if device.ID != "" {
			metricLabels["gpu_index"] = device.ID
		}

		result = append(result, canonicalSample{
			name:   "neutree_node_gpu_info",
			labels: metricLabels,
			value:  1,
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
		metricLabels := nodeGPULabels(labels, product)

		result = append(result,
			canonicalSample{
				name:   "neutree_node_gpu_total",
				labels: cloneLabels(metricLabels),
				value:  total,
			},
			canonicalSample{
				name:   "neutree_node_gpu_allocated",
				labels: cloneLabels(metricLabels),
				value:  allocated,
			},
			canonicalSample{
				name:   "neutree_node_gpu_free",
				labels: cloneLabels(metricLabels),
				value:  free,
			},
		)
	}

	return result
}

func normalizeGPUHardwareInfoSamples(labels CanonicalLabels, infos []GPUHardwareInfo) []canonicalSample {
	result := make([]canonicalSample, 0, len(infos))

	for _, info := range infos {
		if info.UUID == "" {
			continue
		}

		metricLabels := nodeGPULabels(labels, hardwareInfoLabelValue(info.Product))
		metricLabels["gpu_uuid"] = info.UUID
		metricLabels["gpu_index"] = hardwareInfoLabelValue(info.Index)
		metricLabels["architecture"] = hardwareInfoLabelValue(info.Architecture)
		metricLabels["cuda_capability"] = hardwareInfoLabelValue(info.CUDACapability)
		metricLabels["driver_version"] = hardwareInfoLabelValue(info.DriverVersion)
		metricLabels["cuda_driver_version"] = hardwareInfoLabelValue(info.CUDADriverVersion)
		metricLabels["memory_total_mib"] = hardwareInfoLabelValue(info.MemoryTotalMiB)
		metricLabels["nvlink"] = hardwareInfoLabelValue(info.NVLink)
		metricLabels["nvswitch"] = hardwareInfoLabelValue(info.NVSwitch)
		metricLabels["pcie_bus_id"] = hardwareInfoLabelValue(info.PCIEBusID)
		metricLabels["pcie_generation"] = hardwareInfoLabelValue(info.PCIEGeneration)
		metricLabels["pcie_width"] = hardwareInfoLabelValue(info.PCIEWidth)
		metricLabels["numa_node"] = hardwareInfoLabelValue(info.NUMANode)

		result = append(result, canonicalSample{
			name:   "neutree_node_gpu_hardware_info",
			labels: metricLabels,
			value:  1,
		})
	}

	return result
}

func normalizeEndpointAllocationSamples(
	labels CanonicalLabels,
	allocations []EndpointAllocation,
) []canonicalSample {
	result := make([]canonicalSample, 0)

	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID == "" {
				continue
			}

			metricLabels := baseLabels(labels, SourceNodeAgent)
			metricLabels["workspace"] = firstNonEmpty(allocation.Workspace, labels.Workspace)
			metricLabels["neutree_cluster"] = firstNonEmpty(allocation.Cluster, labels.NeutreeCluster, labels.StaticNodeCluster)
			metricLabels["endpoint"] = allocation.Endpoint
			metricLabels["instance_id"] = allocation.InstanceID
			metricLabels["replica_id"] = allocation.ReplicaID
			metricLabels["node"] = firstNonEmpty(allocation.NodeID, device.NodeID, labels.Node)
			metricLabels["gpu_uuid"] = device.UUID
			metricLabels["product"] = device.Product
			metricLabels["resource_mode"] = deviceAllocationResourceMode(device)

			result = append(result, canonicalSample{
				name:   "neutree_endpoint_replica_gpu_allocation",
				labels: metricLabels,
				value:  1,
			})
			if device.MemoryMiB > 0 {
				result = append(result, canonicalSample{
					name:   "neutree_endpoint_replica_gpu_memory_allocated_bytes",
					labels: metricLabels,
					value:  mibToBytes(device.MemoryMiB),
				})
			}

			if device.UsedMemoryMiB > 0 {
				result = append(result, canonicalSample{
					name:   "neutree_endpoint_replica_gpu_memory_used_bytes",
					labels: metricLabels,
					value:  mibToBytes(device.UsedMemoryMiB),
				})
			}

			nodeGPUMetricLabels := cloneLabels(metricLabels)
			nodeGPUMetricLabels["replica"] = allocation.ReplicaID

			result = append(result, canonicalSample{
				name:   "neutree_node_gpu_allocation",
				labels: nodeGPUMetricLabels,
				value:  1,
			})
			if device.MemoryMiB > 0 {
				result = append(result, canonicalSample{
					name:   "neutree_node_gpu_allocation_memory_allocated_bytes",
					labels: nodeGPUMetricLabels,
					value:  mibToBytes(device.MemoryMiB),
				})
			}

			if device.UsedMemoryMiB > 0 {
				result = append(result, canonicalSample{
					name:   "neutree_node_gpu_allocation_memory_used_bytes",
					labels: nodeGPUMetricLabels,
					value:  mibToBytes(device.UsedMemoryMiB),
				})
			}
		}
	}

	return result
}

func normalizeNodeGPUAllocationInfoSamples(
	labels CanonicalLabels,
	raw string,
	allocations []EndpointAllocation,
	hardwareInfos []GPUHardwareInfo,
) []canonicalSample {
	devices := acceleratorDevicesFromMetrics(raw)
	if len(devices) == 0 {
		return nil
	}

	memoryByUUID := gpuMemorySnapshotByUUID(parsePrometheusText(raw))
	hardwareByUUID := gpuHardwareInfoByUUID(hardwareInfos)
	allocationsByUUID := endpointAllocationsByGPUUUID(allocations)
	result := make([]canonicalSample, 0, len(devices))

	for _, device := range devices {
		if device.UUID == "" {
			continue
		}

		product := firstNonEmpty(device.ProductModel, device.ProductName, v1.AcceleratorTypeNVIDIAGPU.String())
		deviceLabels := nodeGPULabels(labels, product)
		deviceLabels["gpu_uuid"] = device.UUID
		deviceLabels["gpu_index"] = firstNonEmpty(device.ID, hardwareByUUID[device.UUID].Index, "-")
		deviceLabels["node_gpu"] = nodeGPUDisplay(deviceLabels["node"], deviceLabels["gpu_index"])

		physicalUsed := memoryByUUID[device.UUID].usedBytes
		physicalTotal := firstPositiveFloat(
			memoryByUUID[device.UUID].totalBytes,
			mibStringToBytes(hardwareByUUID[device.UUID].MemoryTotalMiB),
			mibToBytes(device.MemoryMiB),
		)
		deviceLabels["physical_vram"] = bytesPairDisplay(physicalUsed, physicalTotal)

		deviceAllocations := allocationsByUUID[device.UUID]
		if len(deviceAllocations) == 0 {
			unallocatedLabels := cloneLabels(deviceLabels)
			unallocatedLabels["endpoint"] = "-"
			unallocatedLabels["replica"] = "-"
			unallocatedLabels["replica_id"] = "-"
			unallocatedLabels["endpoint_replica"] = "-"
			unallocatedLabels["instance_id"] = "-"
			unallocatedLabels["vram"] = "-"
			unallocatedLabels["resource_mode"] = v1.DeviceAllocationResourceModePhysicalGPU

			result = append(result, canonicalSample{
				name:   "neutree_node_gpu_allocation_info",
				labels: unallocatedLabels,
				value:  1,
			})

			continue
		}

		for _, allocation := range deviceAllocations {
			allocationLabels := cloneLabels(deviceLabels)
			allocationLabels["workspace"] = firstNonEmpty(allocation.allocation.Workspace, labels.Workspace)
			allocationLabels["neutree_cluster"] = firstNonEmpty(
				allocation.allocation.Cluster,
				labels.NeutreeCluster,
				labels.StaticNodeCluster,
			)
			allocationLabels["endpoint"] = nonEmptyOrDash(allocation.allocation.Endpoint)
			allocationLabels["replica"] = nonEmptyOrDash(allocation.allocation.ReplicaID)
			allocationLabels["replica_id"] = nonEmptyOrDash(allocation.allocation.ReplicaID)
			allocationLabels["instance_id"] = nonEmptyOrDash(allocation.allocation.InstanceID)
			allocationLabels["node"] = firstNonEmpty(allocation.allocation.NodeID, allocation.device.NodeID, labels.Node)
			allocationLabels["node_gpu"] = nodeGPUDisplay(allocationLabels["node"], deviceLabels["gpu_index"])
			allocationLabels["product"] = firstNonEmpty(allocation.device.Product, product)
			allocationLabels["resource_mode"] = deviceAllocationResourceMode(allocation.device)
			allocationLabels["endpoint_replica"] = endpointReplicaDisplay(
				allocationLabels["endpoint"],
				allocationLabels["replica"],
			)
			allocationLabels["vram"] = bytesPairDisplay(
				mibToBytes(allocation.device.UsedMemoryMiB),
				mibToBytes(allocation.device.MemoryMiB),
			)

			result = append(result, canonicalSample{
				name:   "neutree_node_gpu_allocation_info",
				labels: allocationLabels,
				value:  1,
			})
		}
	}

	return result
}

func deviceAllocationResourceMode(device v1.DeviceAllocation) string {
	if device.ResourceMode != "" {
		return device.ResourceMode
	}

	return v1.DeviceAllocationResourceModePhysicalGPU
}

type gpuMemorySnapshot struct {
	usedBytes  float64
	totalBytes float64
}

func gpuMemorySnapshotByUUID(samples []sample) map[string]gpuMemorySnapshot {
	result := map[string]gpuMemorySnapshot{}

	for _, s := range samples {
		uuid := firstNonEmpty(s.labels["UUID"], s.labels["uuid"])
		if uuid == "" {
			continue
		}

		snapshot := result[uuid]

		switch s.name {
		case "DCGM_FI_DEV_FB_USED":
			snapshot.usedBytes = s.value * 1024 * 1024
		case "DCGM_FI_DEV_FB_TOTAL":
			snapshot.totalBytes = s.value * 1024 * 1024
		}

		result[uuid] = snapshot
	}

	return result
}

func gpuHardwareInfoByUUID(infos []GPUHardwareInfo) map[string]GPUHardwareInfo {
	result := map[string]GPUHardwareInfo{}

	for _, info := range infos {
		if info.UUID == "" {
			continue
		}

		result[info.UUID] = info
	}

	return result
}

type endpointDeviceAllocation struct {
	allocation EndpointAllocation
	device     v1.DeviceAllocation
}

func endpointAllocationsByGPUUUID(allocations []EndpointAllocation) map[string][]endpointDeviceAllocation {
	result := map[string][]endpointDeviceAllocation{}

	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID == "" {
				continue
			}

			result[device.UUID] = append(result[device.UUID], endpointDeviceAllocation{
				allocation: allocation,
				device:     device,
			})
		}
	}

	return result
}

func mibToBytes(value int64) float64 {
	return float64(value) * 1024 * 1024
}

func mibStringToBytes(value string) float64 {
	mib, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}

	return mib * 1024 * 1024
}

func firstPositiveFloat(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}

	return 0
}

func bytesPairDisplay(usedBytes, totalBytes float64) string {
	if usedBytes <= 0 && totalBytes <= 0 {
		return "-"
	}

	return fmt.Sprintf("%s / %s", bytesDisplay(usedBytes), bytesDisplay(totalBytes))
}

func bytesDisplay(value float64) string {
	if value <= 0 {
		return "-"
	}

	const gib = 1024 * 1024 * 1024

	return fmt.Sprintf("%.1f GiB", value/gib)
}

func endpointReplicaDisplay(endpoint, replica string) string {
	if endpoint == "" || endpoint == "-" || replica == "" || replica == "-" {
		return "-"
	}

	return endpoint + " / " + replica
}

func nodeGPUDisplay(node, gpuIndex string) string {
	node = nonEmptyOrDash(node)
	gpuIndex = nonEmptyOrDash(gpuIndex)

	if node == "-" && gpuIndex == "-" {
		return "-"
	}

	if gpuIndex == "-" {
		return node + " / GPU -"
	}

	return node + " / GPU " + gpuIndex
}

func nonEmptyOrDash(value string) string {
	if value == "" {
		return "-"
	}

	return value
}

func normalizeEndpointReplicaRuntimeUsageSamples(
	labels CanonicalLabels,
	usages []EndpointReplicaRuntimeUsage,
) []canonicalSample {
	result := make([]canonicalSample, 0, len(usages)*5)

	for _, usage := range usages {
		metricLabels := endpointReplicaRuntimeUsageLabels(labels, usage)
		result = append(result, canonicalSample{
			name:   "neutree_endpoint_replica_cpu_usage_seconds_total",
			labels: metricLabels,
			value:  usage.CPUUsageSeconds,
		})

		if usage.MemoryUsageBytes != nil {
			result = append(result, canonicalSample{
				name:   "neutree_endpoint_replica_memory_usage_bytes",
				labels: metricLabels,
				value:  *usage.MemoryUsageBytes,
			})
		}

		if usage.MemoryWorkingSetBytes != nil {
			result = append(result, canonicalSample{
				name:   "neutree_endpoint_replica_memory_working_set_bytes",
				labels: metricLabels,
				value:  *usage.MemoryWorkingSetBytes,
			})
		}

		if usage.CPULimitCores != nil {
			result = append(result, canonicalSample{
				name:   "neutree_endpoint_replica_cpu_limit_cores",
				labels: metricLabels,
				value:  *usage.CPULimitCores,
			})
		}

		if usage.MemoryLimitBytes != nil {
			result = append(result, canonicalSample{
				name:   "neutree_endpoint_replica_memory_limit_bytes",
				labels: metricLabels,
				value:  *usage.MemoryLimitBytes,
			})
		}
	}

	return result
}

func endpointReplicaRuntimeUsageLabels(
	labels CanonicalLabels,
	usage EndpointReplicaRuntimeUsage,
) map[string]string {
	metricLabels := baseLabels(labels, SourceNodeAgent)
	metricLabels["workspace"] = firstNonEmpty(usage.Workspace, labels.Workspace)
	metricLabels["neutree_cluster"] = firstNonEmpty(usage.Cluster, labels.NeutreeCluster, labels.StaticNodeCluster)
	metricLabels["endpoint"] = usage.Endpoint
	metricLabels["instance_id"] = usage.InstanceID
	metricLabels["replica_id"] = usage.ReplicaID
	metricLabels["replica"] = usage.ReplicaID
	metricLabels["node"] = firstNonEmpty(usage.NodeID, labels.Node)
	metricLabels["deployment"] = usage.Deployment
	metricLabels["container"] = usage.Container
	metricLabels["container_id"] = usage.ContainerID
	metricLabels["engine"] = usage.Engine
	metricLabels["engine_version"] = usage.EngineVersion

	return metricLabels
}

func nodeReadySample(labels CanonicalLabels) canonicalSample {
	metricLabels := baseLabels(labels, SourceNodeAgent)
	metricLabels["neutree_cluster"] = firstNonEmpty(labels.NeutreeCluster, labels.StaticNodeCluster)

	return canonicalSample{
		name:   "neutree_node_ready",
		labels: metricLabels,
		value:  1,
	}
}

func scrapeUpSample(labels CanonicalLabels, target string, up bool) canonicalSample {
	value := float64(0)
	if up {
		value = 1
	}

	metricLabels := baseLabels(labels, SourceNodeAgent)
	metricLabels["target"] = target

	return canonicalSample{
		name:   "neutree_metrics_scrape_up",
		labels: metricLabels,
		value:  value,
	}
}

func nodeGPULabels(labels CanonicalLabels, product string) map[string]string {
	metricLabels := baseLabels(labels, SourceNodeAgent)
	metricLabels["neutree_cluster"] = firstNonEmpty(labels.NeutreeCluster, labels.StaticNodeCluster)
	metricLabels["accelerator_type"] = v1.AcceleratorTypeNVIDIAGPU.String()
	metricLabels["product"] = product

	return metricLabels
}

func allocatedDeviceUUIDs(allocations []EndpointAllocation) map[string]struct{} {
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

func baseLabels(labels CanonicalLabels, source string) map[string]string {
	clusterType := labels.ClusterType
	if clusterType == "" {
		clusterType = "ray"
	}

	return map[string]string{
		"workspace":           labels.Workspace,
		"static_node_cluster": labels.StaticNodeCluster,
		"cluster_type":        clusterType,
		"node":                labels.Node,
		"node_ip":             labels.NodeIP,
		"node_role":           labels.NodeRole,
		"source":              source,
	}
}

func acceleratorMetricLabels(labels CanonicalLabels, s sample) (map[string]string, bool) {
	uuid := firstNonEmpty(s.labels["UUID"], s.labels["uuid"])
	if uuid == "" {
		return nil, false
	}

	result := baseLabels(labels, TargetAcceleratorExporter)
	result["neutree_cluster"] = firstNonEmpty(labels.NeutreeCluster, labels.StaticNodeCluster)
	result["gpu_uuid"] = uuid

	if gpuIndex := firstNonEmpty(s.labels["gpu"], s.labels["GPU_I_ID"]); gpuIndex != "" {
		result["gpu_index"] = gpuIndex
	}

	if model := firstNonEmpty(s.labels["modelName"], s.labels["model"]); model != "" {
		result["model"] = model
	}

	return result, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

func parsePrometheusText(raw string) []sample {
	var result []sample

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		metricPart, valuePart, ok := splitPrometheusSampleLine(line)
		if !ok {
			continue
		}

		value, err := strconv.ParseFloat(strings.Fields(valuePart)[0], 64)
		if err != nil {
			continue
		}

		name, labels := parseMetricPart(metricPart)
		if name == "" {
			continue
		}

		result = append(result, sample{name: name, labels: labels, value: value})
	}

	return result
}

func splitPrometheusSampleLine(line string) (string, string, bool) {
	escaped := false
	inQuote := false

	for index, ch := range line {
		switch {
		case escaped:
			escaped = false
		case ch == '\\':
			escaped = true
		case ch == '"':
			inQuote = !inQuote
		case (ch == ' ' || ch == '\t') && !inQuote:
			metricPart := strings.TrimSpace(line[:index])
			valuePart := strings.TrimSpace(line[index:])

			return metricPart, valuePart, metricPart != "" && valuePart != ""
		}
	}

	return "", "", false
}

func parseMetricPart(metricPart string) (string, map[string]string) {
	openIndex := strings.Index(metricPart, "{")
	if openIndex < 0 {
		return metricPart, nil
	}

	closeIndex := strings.LastIndex(metricPart, "}")
	if closeIndex < openIndex {
		return "", nil
	}

	return metricPart[:openIndex], parseLabels(metricPart[openIndex+1 : closeIndex])
}

func parseLabels(raw string) map[string]string {
	labels := map[string]string{}

	for _, item := range splitLabelItems(raw) {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}

		labels[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}

	return labels
}

func splitLabelItems(raw string) []string {
	var items []string
	var current strings.Builder
	escaped := false
	inQuote := false

	for _, ch := range raw {
		switch {
		case escaped:
			current.WriteRune(ch)

			escaped = false
		case ch == '\\':
			current.WriteRune(ch)

			escaped = true
		case ch == '"':
			current.WriteRune(ch)

			inQuote = !inQuote
		case ch == ',' && !inQuote:
			items = append(items, current.String())
			current.Reset()
		default:
			current.WriteRune(ch)
		}
	}

	if current.Len() > 0 {
		items = append(items, current.String())
	}

	return items
}

func indexFirstSampleByName(samples []sample) map[string]sample {
	result := make(map[string]sample, len(samples))
	for _, s := range samples {
		if _, exists := result[s.name]; exists {
			continue
		}

		result[s.name] = s
	}

	return result
}

func formatSample(s canonicalSample) string {
	return fmt.Sprintf("%s%s %s", s.name, formatLabels(s.labels), formatFloat(s.value))
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
