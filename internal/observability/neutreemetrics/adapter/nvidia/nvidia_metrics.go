package nvidia

import (
	"cmp"
	"sort"
	"strconv"

	prommodel "github.com/prometheus/common/model"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

const (
	nvidiaUnknownLabelValue       = "unknown"
	nvidiaDCGMDeviceGPUUtilMetric = "DCGM_FI_DEV_GPU_UTIL"
	nvidiaDCGMDeviceFBUsedMetric  = "DCGM_FI_DEV_FB_USED"
	nvidiaDCGMDeviceFBTotalMetric = "DCGM_FI_DEV_FB_TOTAL"
)

// nvidiaBuildMetricSamples owns the complete NVIDIA/DCGM rendering path.
// The generic host only merges the returned canonical samples into its
// Prometheus exposition.
func nvidiaBuildMetricSamples(
	labels adapter.CanonicalLabels,
	raw string,
	hardwareInfos []nvidiaHardwareInfo,
	endpointAllocations []model.EndpointAllocation,
	endpointUsages []adapter.EndpointReplicaAcceleratorUsage,
) []adapter.Sample {
	if raw == "" {
		// Keep the existing degraded-mode behavior: without a successful DCGM
		// scrape, only explicit per-replica observations are reportable.
		return nvidiaNormalizeEndpointReplicaUsageSamples(
			labels,
			endpointUsages,
			endpointAllocations,
			nil,
		)
	}

	acceleratorIndexes := nvidiaAcceleratorIndexesByUUID(raw, hardwareInfos)
	samples := make([]adapter.Sample, 0, len(endpointAllocations)*2+8)
	samples = append(samples, nvidiaNormalizeAcceleratorSamples(labels, raw)...)
	samples = append(samples, nvidiaNormalizeNodeGPUSamples(labels, raw, endpointAllocations)...)
	samples = append(samples, nvidiaNormalizeGPUHardwareInfoSamples(labels, hardwareInfos, raw)...)
	samples = append(samples, nvidiaNormalizeEndpointAllocationSamples(
		labels,
		endpointAllocations,
		endpointUsages,
		acceleratorIndexes,
		raw,
	)...)
	samples = append(samples, nvidiaNormalizeEndpointReplicaUsageFromDCGMSamples(
		labels,
		raw,
		endpointAllocations,
		endpointUsages,
	)...)
	samples = append(samples, nvidiaNormalizeEndpointReplicaUsageSamples(
		labels,
		endpointUsages,
		endpointAllocations,
		acceleratorIndexes,
	)...)

	return samples
}

func nvidiaNormalizeAcceleratorSamples(labels adapter.CanonicalLabels, raw string) []adapter.Sample {
	parsed := promtext.ParseVector(raw)
	result := make([]adapter.Sample, 0)

	for _, sample := range parsed {
		metricLabels, ok := nvidiaAcceleratorMetricLabels(labels, sample)
		if !ok {
			continue
		}

		switch promtext.MetricName(sample) {
		case nvidiaDCGMDeviceGPUUtilMetric:
			value := promtext.Value(sample)
			if value > 1 {
				value /= 100
			}

			result = append(result, adapter.Sample{
				Name:   "neutree_accelerator_utilization_ratio",
				Labels: metricLabels,
				Value:  value,
			})
		case nvidiaDCGMDeviceFBUsedMetric:
			result = append(result, adapter.Sample{
				Name:   "neutree_accelerator_memory_used_bytes",
				Labels: metricLabels,
				Value:  promtext.Value(sample) * 1024 * 1024,
			})
		case nvidiaDCGMDeviceFBTotalMetric:
			result = append(result, adapter.Sample{
				Name:   "neutree_accelerator_memory_total_bytes",
				Labels: metricLabels,
				Value:  promtext.Value(sample) * 1024 * 1024,
			})
		case "DCGM_FI_DEV_GPU_TEMP":
			result = append(result, adapter.Sample{
				Name:   "neutree_accelerator_temperature_celsius",
				Labels: metricLabels,
				Value:  promtext.Value(sample),
			})
		case "DCGM_FI_PROF_PCIE_TX_BYTES":
			result = append(result, adapter.Sample{
				Name:   "neutree_accelerator_pcie_tx_bytes_total",
				Labels: metricLabels,
				Value:  promtext.Value(sample),
			})
		case "DCGM_FI_PROF_PCIE_RX_BYTES":
			result = append(result, adapter.Sample{
				Name:   "neutree_accelerator_pcie_rx_bytes_total",
				Labels: metricLabels,
				Value:  promtext.Value(sample),
			})
		}
	}

	return result
}

func nvidiaNormalizeNodeGPUSamples(
	labels adapter.CanonicalLabels,
	raw string,
	allocations []model.EndpointAllocation,
) []adapter.Sample {
	devices := nvidiaDevicesFromDCGM(raw)
	if len(devices) == 0 {
		return nil
	}

	allocatedByUUID := nvidiaAllocatedDeviceUUIDs(allocations)
	totalByProduct := map[string]float64{}
	allocatedByProduct := map[string]float64{}
	result := make([]adapter.Sample, 0, len(devices)*2)

	for _, device := range devices {
		if device.UUID == "" {
			continue
		}

		product := cmp.Or(device.ProductModel, device.ProductName)
		totalByProduct[product]++

		if _, ok := allocatedByUUID[device.UUID]; ok {
			allocatedByProduct[product]++
		}

		result = append(result, adapter.Sample{
			Name: "neutree_node_accelerator_info",
			Labels: nvidiaPhysicalAcceleratorLabels(
				labels,
				v1.AcceleratorTypeNVIDIAGPU.String(),
				device.UUID,
				device.ID,
				product,
			),
			Value: 1,
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
		metricLabels := nvidiaNodeAcceleratorProductLabels(labels, v1.AcceleratorTypeNVIDIAGPU.String(), product)

		result = append(result,
			adapter.Sample{
				Name:   "neutree_node_accelerator_total",
				Labels: adapter.CloneStringMap(metricLabels),
				Value:  total,
			},
			adapter.Sample{
				Name:   "neutree_node_accelerator_allocated",
				Labels: adapter.CloneStringMap(metricLabels),
				Value:  allocated,
			},
			adapter.Sample{
				Name:   "neutree_node_accelerator_free",
				Labels: adapter.CloneStringMap(metricLabels),
				Value:  free,
			},
		)
	}

	return result
}

func nvidiaNormalizeGPUHardwareInfoSamples(
	labels adapter.CanonicalLabels,
	infos []nvidiaHardwareInfo,
	raw string,
) []adapter.Sample {
	discoveredUUIDs := nvidiaDiscoveredGPUUUIDs(raw)
	if len(discoveredUUIDs) == 0 {
		return nil
	}

	result := make([]adapter.Sample, 0, len(infos)*2)

	for _, info := range infos {
		if info.UUID == "" {
			continue
		}

		if _, ok := discoveredUUIDs[info.UUID]; !ok {
			continue
		}

		commonLabels := nvidiaPhysicalAcceleratorLabels(
			labels,
			v1.AcceleratorTypeNVIDIAGPU.String(),
			info.UUID,
			info.Index,
			info.Product,
		)
		commonLabels["memory_total_bytes"] = nvidiaMemoryMiBLabelToBytes(info.MemoryTotalMiB)
		commonLabels["pcie_bus_id"] = nvidiaHardwareLabelValue(info.PCIEBusID)
		commonLabels["pcie_generation"] = nvidiaHardwareLabelValue(info.PCIEGeneration)
		commonLabels["pcie_width"] = nvidiaHardwareLabelValue(info.PCIEWidth)
		commonLabels["numa_node"] = nvidiaHardwareLabelValue(info.NUMANode)

		nvidiaLabels := nvidiaPhysicalAcceleratorLabels(
			labels,
			v1.AcceleratorTypeNVIDIAGPU.String(),
			info.UUID,
			info.Index,
			info.Product,
		)
		nvidiaLabels["architecture"] = nvidiaHardwareLabelValue(info.Architecture)
		nvidiaLabels["cuda_capability"] = nvidiaHardwareLabelValue(info.CUDACapability)
		nvidiaLabels["driver_version"] = nvidiaHardwareLabelValue(info.DriverVersion)
		nvidiaLabels["cuda_driver_version"] = nvidiaHardwareLabelValue(info.CUDADriverVersion)
		nvidiaLabels["nvlink"] = nvidiaHardwarePresenceLabelValue(info.NVLink)
		nvidiaLabels["nvswitch"] = nvidiaHardwarePresenceLabelValue(info.NVSwitch)

		result = append(result,
			adapter.Sample{
				Name:   "neutree_node_accelerator_hardware_info",
				Labels: commonLabels,
				Value:  1,
			},
			adapter.Sample{
				Name:   "neutree_node_accelerator_nvidia_info",
				Labels: nvidiaLabels,
				Value:  1,
			},
		)
	}

	return result
}

func nvidiaDevicesFromDCGM(raw string) []v1.StaticNodeAcceleratorDeviceStatus {
	devicesByUUID := map[string]v1.StaticNodeAcceleratorDeviceStatus{}
	discoveredUUIDs := map[string]struct{}{}

	for _, metric := range promtext.ParseVector(raw) {
		uuid := promtext.LabelValue(metric, "UUID", "uuid")
		if uuid == "" {
			continue
		}

		if promtext.MetricName(metric) == nvidiaDCGMDeviceGPUUtilMetric {
			discoveredUUIDs[uuid] = struct{}{}
		}

		device := devicesByUUID[uuid]
		device.UUID = uuid
		device.Healthy = true

		if id := promtext.LabelValue(metric, "gpu", "GPU_I_ID"); id != "" {
			device.ID = id
			if minorNumber, err := strconv.Atoi(id); err == nil {
				device.MinorNumber = &minorNumber
			}
		}

		if modelName := promtext.LabelValue(metric, "modelName", "model"); modelName != "" {
			device.ProductName = modelName
			device.ProductModel = modelName
		}

		if promtext.MetricName(metric) == nvidiaDCGMDeviceFBTotalMetric && promtext.Value(metric) > 0 {
			device.MemoryMiB = int64(promtext.Value(metric))
		}

		devicesByUUID[uuid] = device
	}

	devices := make([]v1.StaticNodeAcceleratorDeviceStatus, 0, len(discoveredUUIDs))

	for _, device := range devicesByUUID {
		if _, ok := discoveredUUIDs[device.UUID]; ok {
			devices = append(devices, device)
		}
	}

	return devices
}

func nvidiaDiscoveredGPUUUIDs(raw string) map[string]struct{} {
	result := map[string]struct{}{}

	for _, device := range nvidiaDevicesFromDCGM(raw) {
		if device.UUID != "" {
			result[device.UUID] = struct{}{}
		}
	}

	return result
}

func nvidiaPhysicalAcceleratorLabels(
	labels adapter.CanonicalLabels,
	acceleratorType string,
	uuid string,
	acceleratorIndex string,
	product string,
) map[string]string {
	metricLabels := nvidiaAcceleratorBaseLabels(labels)
	metricLabels["accelerator_type"] = nvidiaLabelValueOrUnknown(acceleratorType)
	metricLabels["accelerator_uuid"] = uuid
	metricLabels["accelerator_index"] = nvidiaLabelValueOrUnknown(acceleratorIndex)
	metricLabels["product"] = nvidiaLabelValueOrUnknown(product)

	return metricLabels
}

func nvidiaNodeAcceleratorProductLabels(
	labels adapter.CanonicalLabels,
	acceleratorType string,
	product string,
) map[string]string {
	metricLabels := nvidiaAcceleratorBaseLabels(labels)
	metricLabels["accelerator_type"] = nvidiaLabelValueOrUnknown(acceleratorType)
	metricLabels["product"] = nvidiaLabelValueOrUnknown(product)

	return metricLabels
}

func nvidiaEndpointAcceleratorLabels(
	labels adapter.CanonicalLabels,
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
		"cluster_type":      nvidiaLabelValueOrUnknown(labels.ClusterType),
		"endpoint":          nvidiaLabelValueOrUnknown(endpoint),
		"instance_id":       nvidiaLabelValueOrUnknown(instanceID),
		"replica":           nvidiaLabelValueOrUnknown(replicaID),
		"node":              nvidiaLabelValueOrUnknown(cmp.Or(node, labels.Node)),
		"accelerator_type":  nvidiaLabelValueOrUnknown(acceleratorType),
		"accelerator_uuid":  uuid,
		"accelerator_index": nvidiaLabelValueOrUnknown(acceleratorIndex),
		"vdevice_index":     nvidiaVDeviceIndexOrDefault(vdeviceIndex),
		"product":           nvidiaLabelValueOrUnknown(product),
	}
}

func nvidiaAcceleratorBaseLabels(labels adapter.CanonicalLabels) map[string]string {
	return map[string]string{
		"cluster_type": nvidiaLabelValueOrUnknown(labels.ClusterType),
		"node":         nvidiaLabelValueOrUnknown(labels.Node),
	}
}

func nvidiaAcceleratorMetricLabels(
	labels adapter.CanonicalLabels,
	sample *prommodel.Sample,
) (map[string]string, bool) {
	uuid := promtext.LabelValue(sample, "UUID", "uuid")
	if uuid == "" {
		return nil, false
	}

	return nvidiaPhysicalAcceleratorLabels(
		labels,
		v1.AcceleratorTypeNVIDIAGPU.String(),
		uuid,
		promtext.LabelValue(sample, "gpu", "GPU_I_ID"),
		promtext.LabelValue(sample, "modelName", "model"),
	), true
}
