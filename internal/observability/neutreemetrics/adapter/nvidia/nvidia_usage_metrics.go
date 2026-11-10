package nvidia

import (
	"cmp"
	"math"
	"strconv"
	"strings"

	prommodel "github.com/prometheus/common/model"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func nvidiaNormalizeEndpointAllocationSamples(
	labels adapter.CanonicalLabels,
	allocations []model.EndpointAllocation,
	explicitUsages []adapter.EndpointReplicaAcceleratorUsage,
	acceleratorIndexes map[string]string,
	acceleratorRaw string,
) []adapter.Sample {
	result := make([]adapter.Sample, 0)
	physicalVRAMs := nvidiaPhysicalVRAMByUUID(acceleratorRaw)
	uniqueAllocations := nvidiaUniqueEndpointAllocationsByUUID(allocations)
	explicitUsageMemoryUsedBytes := nvidiaExplicitUsageMemoryUsedBytes(labels, explicitUsages)

	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID == "" {
				continue
			}

			var derivedUsedBytes *float64
			physicalVRAM := physicalVRAMs[device.UUID]

			if device.UsedMemoryMiB <= 0 {
				if explicitUsedBytes, ok := nvidiaExplicitUsageMemoryUsedBytesForAllocation(
					explicitUsageMemoryUsedBytes,
					labels,
					allocation,
					device,
				); ok {
					derivedUsedBytes = &explicitUsedBytes
				} else if _, ok := uniqueAllocations[device.UUID]; ok && physicalVRAM.hasUsed {
					derivedUsedBytes = &physicalVRAM.usedBytes
				}
			}

			metricLabels := nvidiaEndpointAllocationLabels(labels, allocation, device, acceleratorIndexes[device.UUID])
			result = append(result, adapter.Sample{
				Name:   "neutree_endpoint_replica_accelerator_allocation",
				Labels: nvidiaAllocationInfoLabels(metricLabels, device, physicalVRAM, derivedUsedBytes),
				Value:  1,
			})

			if device.MemoryMiB > 0 {
				result = append(result, adapter.Sample{
					Name:   "neutree_endpoint_replica_accelerator_memory_allocated_bytes",
					Labels: metricLabels,
					Value:  nvidiaMiBToBytes(device.MemoryMiB),
				})
			}

			if device.UsedMemoryMiB > 0 {
				result = append(result, adapter.Sample{
					Name:   "neutree_endpoint_replica_accelerator_memory_used_bytes",
					Labels: metricLabels,
					Value:  nvidiaMiBToBytes(device.UsedMemoryMiB),
				})
			}
		}
	}

	return result
}

func nvidiaEndpointAllocationLabels(
	labels adapter.CanonicalLabels,
	allocation model.EndpointAllocation,
	device v1.DeviceAllocation,
	acceleratorIndex string,
) map[string]string {
	return nvidiaEndpointAcceleratorLabels(
		labels,
		allocation.Endpoint,
		allocation.InstanceID,
		allocation.ReplicaID,
		cmp.Or(allocation.NodeID, device.NodeID, labels.Node),
		v1.AcceleratorTypeNVIDIAGPU.String(),
		device.UUID,
		acceleratorIndex,
		device.VDeviceIndex,
		device.Product,
	)
}

type nvidiaVRAMSnapshot struct {
	usedBytes  float64
	totalBytes float64
	hasUsed    bool
	hasTotal   bool
}

func nvidiaAllocationInfoLabels(
	base map[string]string,
	device v1.DeviceAllocation,
	physicalVRAM nvidiaVRAMSnapshot,
	derivedUsedBytes *float64,
) map[string]string {
	labels := adapter.CloneStringMap(base)
	labels["vram_usage"] = nvidiaAllocationVRAMLabel(device, physicalVRAM, derivedUsedBytes)
	labels["physical_vram_usage"] = nvidiaVRAMLabel(physicalVRAM)

	return labels
}

func nvidiaAllocationVRAMLabel(
	device v1.DeviceAllocation,
	physicalVRAM nvidiaVRAMSnapshot,
	derivedUsedBytes *float64,
) string {
	usedBytes := nvidiaMiBToBytes(device.UsedMemoryMiB)

	if usedBytes <= 0 {
		if derivedUsedBytes == nil {
			return nvidiaUnknownLabelValue
		}

		usedBytes = *derivedUsedBytes
	}

	if usedBytes <= 0 {
		return nvidiaUnknownLabelValue
	}

	allocatedBytes := nvidiaMiBToBytes(device.MemoryMiB)
	if allocatedBytes <= 0 {
		if !physicalVRAM.hasTotal {
			return nvidiaUnknownLabelValue
		}

		allocatedBytes = physicalVRAM.totalBytes
	}

	return nvidiaDisplayBytes(usedBytes) + " / " + nvidiaDisplayBytes(allocatedBytes)
}

func nvidiaVRAMLabel(snapshot nvidiaVRAMSnapshot) string {
	if !snapshot.hasUsed || !snapshot.hasTotal {
		return nvidiaUnknownLabelValue
	}

	return nvidiaDisplayBytes(snapshot.usedBytes) + " / " + nvidiaDisplayBytes(snapshot.totalBytes)
}

func nvidiaPhysicalVRAMByUUID(raw string) map[string]nvidiaVRAMSnapshot {
	result := map[string]nvidiaVRAMSnapshot{}

	for _, sample := range promtext.ParseVector(raw) {
		uuid := promtext.LabelValue(sample, "UUID", "uuid")
		if uuid == "" {
			continue
		}

		snapshot := result[uuid]

		switch promtext.MetricName(sample) {
		case nvidiaDCGMDeviceFBUsedMetric:
			snapshot.usedBytes = promtext.Value(sample) * 1024 * 1024
			snapshot.hasUsed = true
		case nvidiaDCGMDeviceFBTotalMetric:
			snapshot.totalBytes = promtext.Value(sample) * 1024 * 1024
			snapshot.hasTotal = true
		}

		result[uuid] = snapshot
	}

	return result
}

type nvidiaGPUUsageSnapshot struct {
	memoryUsedBytes  *float64
	utilizationRatio *float64
	acceleratorIndex string
}

func nvidiaGPUUsageSnapshotByUUID(samples prommodel.Vector) map[string]nvidiaGPUUsageSnapshot {
	result := map[string]nvidiaGPUUsageSnapshot{}

	for _, sample := range samples {
		uuid := promtext.LabelValue(sample, "UUID", "uuid")
		if uuid == "" {
			continue
		}

		snapshot := result[uuid]
		if index := promtext.LabelValue(sample, "gpu", "GPU_I_ID"); index != "" {
			snapshot.acceleratorIndex = index
		}

		switch promtext.MetricName(sample) {
		case nvidiaDCGMDeviceFBUsedMetric:
			value := promtext.Value(sample) * 1024 * 1024
			snapshot.memoryUsedBytes = &value
		case nvidiaDCGMDeviceGPUUtilMetric:
			value := promtext.Value(sample)
			if value > 1 {
				value /= 100
			}

			snapshot.utilizationRatio = &value
		}

		result[uuid] = snapshot
	}

	return result
}

func nvidiaAcceleratorIndexesByUUID(raw string, infos []nvidiaHardwareInfo) map[string]string {
	result := map[string]string{}

	for _, info := range infos {
		if info.UUID != "" && info.Index != "" {
			result[info.UUID] = info.Index
		}
	}

	for _, sample := range promtext.ParseVector(raw) {
		uuid := promtext.LabelValue(sample, "UUID", "uuid")
		index := promtext.LabelValue(sample, "gpu", "GPU_I_ID")

		if uuid != "" && index != "" {
			result[uuid] = index
		}
	}

	return result
}

type nvidiaEndpointDeviceAllocation struct {
	allocation model.EndpointAllocation
	device     v1.DeviceAllocation
}

type nvidiaEndpointReplicaUsageKey struct {
	endpoint     string
	instanceID   string
	replicaID    string
	nodeID       string
	uuid         string
	vdeviceIndex string
}

func nvidiaExplicitUsageUtilizationUUIDs(usages []adapter.EndpointReplicaAcceleratorUsage) map[string]struct{} {
	result := map[string]struct{}{}

	for _, usage := range usages {
		if usage.AcceleratorUUID != "" && usage.UtilizationRatio != nil {
			result[usage.AcceleratorUUID] = struct{}{}
		}
	}

	return result
}

func nvidiaExplicitUsageMemoryUsedBytes(
	labels adapter.CanonicalLabels,
	usages []adapter.EndpointReplicaAcceleratorUsage,
) map[nvidiaEndpointReplicaUsageKey]float64 {
	result := map[nvidiaEndpointReplicaUsageKey]float64{}
	fallbackCounts := map[nvidiaEndpointReplicaUsageKey]int{}
	fallbackValues := map[nvidiaEndpointReplicaUsageKey]float64{}

	for _, usage := range usages {
		if usage.AcceleratorUUID == "" || usage.MemoryUsedBytes == nil {
			continue
		}

		key := nvidiaEndpointReplicaUsageKeyFromUsage(labels, usage)
		result[key] = *usage.MemoryUsedBytes
		key.vdeviceIndex = ""
		fallbackCounts[key]++
		fallbackValues[key] = *usage.MemoryUsedBytes
	}

	for key, count := range fallbackCounts {
		if count == 1 {
			result[key] = fallbackValues[key]
		}
	}

	return result
}

func nvidiaExplicitUsageMemoryUsedBytesForAllocation(
	usages map[nvidiaEndpointReplicaUsageKey]float64,
	labels adapter.CanonicalLabels,
	allocation model.EndpointAllocation,
	device v1.DeviceAllocation,
) (float64, bool) {
	key := nvidiaEndpointReplicaUsageKeyFromAllocation(labels, allocation, device)
	if value, ok := usages[key]; ok {
		return value, true
	}

	key.vdeviceIndex = ""
	value, ok := usages[key]

	return value, ok
}

func nvidiaEndpointReplicaUsageKeyFromAllocation(
	labels adapter.CanonicalLabels,
	allocation model.EndpointAllocation,
	device v1.DeviceAllocation,
) nvidiaEndpointReplicaUsageKey {
	return nvidiaEndpointReplicaUsageKey{
		endpoint:     allocation.Endpoint,
		instanceID:   allocation.InstanceID,
		replicaID:    allocation.ReplicaID,
		nodeID:       cmp.Or(allocation.NodeID, device.NodeID, labels.Node),
		uuid:         device.UUID,
		vdeviceIndex: nvidiaVDeviceIndexOrDefault(device.VDeviceIndex),
	}
}

func nvidiaUniqueEndpointAllocationsByUUID(
	allocations []model.EndpointAllocation,
) map[string]nvidiaEndpointDeviceAllocation {
	counts := map[string]int{}
	result := map[string]nvidiaEndpointDeviceAllocation{}

	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID == "" {
				continue
			}

			counts[device.UUID]++
			result[device.UUID] = nvidiaEndpointDeviceAllocation{allocation: allocation, device: device}
		}
	}

	for uuid, count := range counts {
		if count != 1 {
			delete(result, uuid)
		}
	}

	return result
}

func nvidiaNormalizeEndpointReplicaUsageSamples(
	labels adapter.CanonicalLabels,
	usages []adapter.EndpointReplicaAcceleratorUsage,
	allocations []model.EndpointAllocation,
	acceleratorIndexes map[string]string,
) []adapter.Sample {
	result := make([]adapter.Sample, 0, len(usages)*2)
	allocationContext := nvidiaEndpointReplicaUsageAllocationContext(labels, allocations, acceleratorIndexes)

	for _, usage := range usages {
		if usage.AcceleratorUUID == "" {
			continue
		}

		metricLabels := nvidiaEndpointReplicaUsageLabels(
			labels,
			nvidiaEnrichEndpointReplicaUsage(labels, usage, allocationContext),
		)
		if usage.MemoryUsedBytes != nil {
			result = append(result, adapter.Sample{
				Name:   "neutree_endpoint_replica_accelerator_memory_used_bytes",
				Labels: metricLabels,
				Value:  *usage.MemoryUsedBytes,
			})
		}

		if usage.UtilizationRatio != nil {
			result = append(result, adapter.Sample{
				Name:   "neutree_endpoint_replica_accelerator_utilization_ratio",
				Labels: metricLabels,
				Value:  *usage.UtilizationRatio,
			})
		}
	}

	return result
}

type nvidiaEndpointReplicaUsageContext struct {
	product          string
	acceleratorIndex string
}

func nvidiaEndpointReplicaUsageAllocationContext(
	labels adapter.CanonicalLabels,
	allocations []model.EndpointAllocation,
	acceleratorIndexes map[string]string,
) map[nvidiaEndpointReplicaUsageKey]nvidiaEndpointReplicaUsageContext {
	result := map[nvidiaEndpointReplicaUsageKey]nvidiaEndpointReplicaUsageContext{}
	fallbackCounts := map[nvidiaEndpointReplicaUsageKey]int{}
	fallbackContexts := map[nvidiaEndpointReplicaUsageKey]nvidiaEndpointReplicaUsageContext{}

	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID == "" {
				continue
			}

			key := nvidiaEndpointReplicaUsageKeyFromAllocation(labels, allocation, device)
			context := nvidiaEndpointReplicaUsageContext{
				product:          device.Product,
				acceleratorIndex: acceleratorIndexes[device.UUID],
			}
			result[key] = context

			key.vdeviceIndex = ""
			fallbackCounts[key]++
			fallbackContexts[key] = context
		}
	}

	for key, count := range fallbackCounts {
		if count == 1 {
			result[key] = fallbackContexts[key]
		}
	}

	return result
}

func nvidiaEnrichEndpointReplicaUsage(
	labels adapter.CanonicalLabels,
	usage adapter.EndpointReplicaAcceleratorUsage,
	allocationContext map[nvidiaEndpointReplicaUsageKey]nvidiaEndpointReplicaUsageContext,
) adapter.EndpointReplicaAcceleratorUsage {
	key := nvidiaEndpointReplicaUsageKeyFromUsage(labels, usage)
	context, ok := allocationContext[key]

	if !ok {
		key.vdeviceIndex = ""
		context, ok = allocationContext[key]
	}

	if !ok {
		return usage
	}

	usage.Product = cmp.Or(usage.Product, context.product)
	usage.AcceleratorIndex = cmp.Or(usage.AcceleratorIndex, context.acceleratorIndex)

	return usage
}

func nvidiaEndpointReplicaUsageKeyFromUsage(
	labels adapter.CanonicalLabels,
	usage adapter.EndpointReplicaAcceleratorUsage,
) nvidiaEndpointReplicaUsageKey {
	return nvidiaEndpointReplicaUsageKey{
		endpoint:     usage.Endpoint,
		instanceID:   usage.InstanceID,
		replicaID:    usage.ReplicaID,
		nodeID:       cmp.Or(usage.NodeID, labels.Node),
		uuid:         usage.AcceleratorUUID,
		vdeviceIndex: nvidiaVDeviceIndexOrDefault(usage.VDeviceIndex),
	}
}

func nvidiaNormalizeEndpointReplicaUsageFromDCGMSamples(
	labels adapter.CanonicalLabels,
	raw string,
	allocations []model.EndpointAllocation,
	explicitUsages []adapter.EndpointReplicaAcceleratorUsage,
) []adapter.Sample {
	snapshots := nvidiaGPUUsageSnapshotByUUID(promtext.ParseVector(raw))
	if len(snapshots) == 0 || len(allocations) == 0 {
		return nil
	}

	explicitMemoryUsedBytes := nvidiaExplicitUsageMemoryUsedBytes(labels, explicitUsages)
	explicitUtilizationUUIDs := nvidiaExplicitUsageUtilizationUUIDs(explicitUsages)
	uniqueAllocations := nvidiaUniqueEndpointAllocationsByUUID(allocations)
	result := make([]adapter.Sample, 0, len(uniqueAllocations)*2)

	for uuid, allocation := range uniqueAllocations {
		snapshot := snapshots[uuid]
		if snapshot.memoryUsedBytes == nil && snapshot.utilizationRatio == nil {
			continue
		}

		metricLabels := nvidiaEndpointAllocationLabels(
			labels,
			allocation.allocation,
			allocation.device,
			snapshot.acceleratorIndex,
		)
		if _, explicit := nvidiaExplicitUsageMemoryUsedBytesForAllocation(
			explicitMemoryUsedBytes,
			labels,
			allocation.allocation,
			allocation.device,
		); snapshot.memoryUsedBytes != nil && allocation.device.UsedMemoryMiB == 0 && !explicit {
			result = append(result, adapter.Sample{
				Name:   "neutree_endpoint_replica_accelerator_memory_used_bytes",
				Labels: metricLabels,
				Value:  *snapshot.memoryUsedBytes,
			})
		}

		if _, explicit := explicitUtilizationUUIDs[uuid]; snapshot.utilizationRatio != nil && !explicit {
			result = append(result, adapter.Sample{
				Name:   "neutree_endpoint_replica_accelerator_utilization_ratio",
				Labels: metricLabels,
				Value:  *snapshot.utilizationRatio,
			})
		}
	}

	return result
}

func nvidiaEndpointReplicaUsageLabels(
	labels adapter.CanonicalLabels,
	usage adapter.EndpointReplicaAcceleratorUsage,
) map[string]string {
	return nvidiaEndpointAcceleratorLabels(
		labels,
		usage.Endpoint,
		usage.InstanceID,
		usage.ReplicaID,
		cmp.Or(usage.NodeID, labels.Node),
		cmp.Or(usage.AcceleratorType, v1.AcceleratorTypeNVIDIAGPU.String()),
		usage.AcceleratorUUID,
		usage.AcceleratorIndex,
		usage.VDeviceIndex,
		usage.Product,
	)
}

func nvidiaAllocatedDeviceUUIDs(allocations []model.EndpointAllocation) map[string]struct{} {
	result := map[string]struct{}{}

	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID != "" {
				result[device.UUID] = struct{}{}
			}
		}
	}

	return result
}

func nvidiaMiBToBytes(value int64) float64 {
	return float64(value) * 1024 * 1024
}

func nvidiaDisplayBytes(value float64) string {
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

func nvidiaMemoryMiBLabelToBytes(value string) string {
	mib, err := strconv.ParseFloat(value, 64)
	if err != nil || mib <= 0 {
		return nvidiaUnknownLabelValue
	}

	return nvidiaFormatFloat(mib * 1024 * 1024)
}

func nvidiaLabelValueOrUnknown(value string) string {
	if value == "" {
		return nvidiaUnknownLabelValue
	}

	return value
}

func nvidiaVDeviceIndexOrDefault(value string) string {
	if value == "" {
		return "0"
	}

	return value
}

func nvidiaFormatFloat(value float64) string {
	if math.Trunc(value) == value {
		return strconv.FormatInt(int64(value), 10)
	}

	return strconv.FormatFloat(value, 'f', -1, 64)
}
