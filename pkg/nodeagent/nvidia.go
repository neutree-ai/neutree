package nodeagent

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hardware"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// nvidiaAccelerator is the reference implementation of the Accelerator
// interface. It reuses the normalizer's legacy DCGM conversion functions so the
// adapter path produces output byte-identical to the legacy normalizer path
// (existing DCGM assertions stay green), while validating the adapter
// framework end to end.
type nvidiaAccelerator struct {
	provider hardware.GPUHardwareInfoProvider
}

var (
	_ adapter.Accelerator           = (*nvidiaAccelerator)(nil)
	_ adapter.KubernetesAccelerator = (*nvidiaAccelerator)(nil)
	_ adapter.StaticAccelerator     = (*nvidiaAccelerator)(nil)
)

func (a *nvidiaAccelerator) Type() string {
	return v1.AcceleratorTypeNVIDIAGPU.String()
}

func (a *nvidiaAccelerator) DiscoverHardware(ctx context.Context) (adapter.HardwareSnapshot, error) {
	provider := a.provider
	if provider == nil {
		provider = hardware.NVMLGPUHardwareInfoProvider{}
	}

	infos, err := provider.GPUHardwareInfos(ctx)
	if err != nil {
		return adapter.HardwareSnapshot{
			Accelerator: v1.StaticNodeAcceleratorStatus{Type: a.Type()},
		}, nil
	}

	snapshot := adapter.HardwareSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Type:    a.Type(),
			Devices: make([]v1.StaticNodeAcceleratorDeviceStatus, 0, len(infos)),
		},
		Details: make([]adapter.HardwareDetails, 0, len(infos)),
	}

	for _, info := range infos {
		device := v1.StaticNodeAcceleratorDeviceStatus{
			ID:           info.Index,
			UUID:         info.UUID,
			ProductName:  info.Product,
			ProductModel: info.Product,
			Healthy:      true,
		}

		if info.MemoryTotalMiB != "" {
			var memoryMiB int64
			if _, scanErr := fmt.Sscan(info.MemoryTotalMiB, &memoryMiB); scanErr == nil {
				device.MemoryMiB = memoryMiB
			}
		}

		if info.MinorNumber != "" {
			var minor int
			if _, scanErr := fmt.Sscan(info.MinorNumber, &minor); scanErr == nil {
				device.MinorNumber = &minor
			}
		}

		snapshot.Accelerator.Devices = append(snapshot.Accelerator.Devices, device)
		snapshot.Details = append(snapshot.Details, adapter.HardwareDetails{
			UUID:           info.UUID,
			Architecture:   info.Architecture,
			DriverVersion:  info.DriverVersion,
			PCIEBusID:      info.PCIEBusID,
			PCIEGeneration: info.PCIEGeneration,
			PCIEWidth:      info.PCIEWidth,
			NUMANode:       info.NUMANode,
		})
	}

	return snapshot, nil
}

func (a *nvidiaAccelerator) BuildKubernetesMetrics(
	ctx context.Context,
	hardware adapter.HardwareSnapshot,
	evidence adapter.KubernetesEvidence,
) (adapter.MetricResult, error) {
	allocations := nvidiaKubernetesAllocations(hardware, evidence)
	endpointReplicaGPUUsages := internalEndpointReplicaAcceleratorUsages(
		evidence.Common.EndpointReplicaAcceleratorUsages,
	)
	result := a.buildMetrics(
		ctx,
		hardware,
		evidence.Common,
		nvidiaEndpointAllocations(evidence.Common.Labels, allocations),
		endpointReplicaGPUUsages,
	)
	result.Allocations = allocations

	return result, nil
}

func (a *nvidiaAccelerator) BuildStaticMetrics(
	ctx context.Context,
	hardware adapter.HardwareSnapshot,
	evidence adapter.StaticEvidence,
) (adapter.MetricResult, error) {
	allocations := nvidiaStaticAllocations(hardware, evidence)
	endpointReplicaGPUUsages := internalEndpointReplicaAcceleratorUsages(
		evidence.Common.EndpointReplicaAcceleratorUsages,
	)
	result := a.buildMetrics(
		ctx,
		hardware,
		evidence.Common,
		nvidiaEndpointAllocations(evidence.Common.Labels, allocations),
		endpointReplicaGPUUsages,
	)
	result.Allocations = allocations

	return result, nil
}

func (a *nvidiaAccelerator) buildMetrics(
	_ context.Context,
	hardware adapter.HardwareSnapshot,
	evidence adapter.CommonEvidence,
	endpointAllocations []model.EndpointAllocation,
	endpointReplicaGPUUsages []model.EndpointReplicaGPUUsage,
) adapter.MetricResult {
	labels := internalLabels(evidence.Labels)
	if !evidence.ExporterUp {
		return adapter.MetricResult{
			Samples: adapterSamplesFromNormalizer(normalizer.NormalizeEndpointReplicaGPUUsageSamples(
				labels,
				endpointReplicaGPUUsages,
				endpointAllocations,
				nil,
			)),
		}
	}

	raw := evidence.ExporterText
	hardwareInfos := gpuHardwareInfosFromSnapshot(hardware)
	acceleratorIndexes := normalizer.AcceleratorIndexesByUUID(raw, hardwareInfos)

	samples := make([]normalizer.Sample, 0, len(endpointAllocations)*2+8)
	samples = append(samples, normalizer.NormalizeAcceleratorSamples(labels, raw)...)
	samples = append(samples, normalizer.NormalizeNodeGPUSamples(labels, raw, endpointAllocations)...)
	samples = append(samples, normalizer.NormalizeGPUHardwareInfoSamples(labels, hardwareInfos, raw)...)
	samples = append(samples, normalizer.NormalizeEndpointAllocationSamples(
		labels,
		endpointAllocations,
		endpointReplicaGPUUsages,
		acceleratorIndexes,
		raw,
	)...)
	samples = append(samples, normalizer.NormalizeEndpointReplicaGPUUsageFromDCGMSamples(
		labels,
		raw,
		endpointAllocations,
		endpointReplicaGPUUsages,
	)...)
	samples = append(samples, normalizer.NormalizeEndpointReplicaGPUUsageSamples(
		labels,
		endpointReplicaGPUUsages,
		endpointAllocations,
		acceleratorIndexes,
	)...)

	return adapter.MetricResult{Samples: adapterSamplesFromNormalizer(samples)}
}

func gpuHardwareInfosFromSnapshot(hardware adapter.HardwareSnapshot) []model.GPUHardwareInfo {
	if len(hardware.Accelerator.Devices) == 0 {
		return nil
	}

	detailsByUUID := map[string]adapter.HardwareDetails{}

	for _, detail := range hardware.Details {
		if detail.UUID != "" {
			detailsByUUID[detail.UUID] = detail
		}
	}

	result := make([]model.GPUHardwareInfo, 0, len(hardware.Accelerator.Devices))

	for _, device := range hardware.Accelerator.Devices {
		detail := detailsByUUID[device.UUID]
		result = append(result, model.GPUHardwareInfo{
			UUID:           device.UUID,
			Index:          device.ID,
			Product:        model.FirstNonEmpty(device.ProductModel, device.ProductName),
			Architecture:   detail.Architecture,
			DriverVersion:  detail.DriverVersion,
			MemoryTotalMiB: fmt.Sprintf("%d", device.MemoryMiB),
			PCIEBusID:      detail.PCIEBusID,
			PCIEGeneration: detail.PCIEGeneration,
			PCIEWidth:      detail.PCIEWidth,
			NUMANode:       detail.NUMANode,
		})
	}

	return result
}

type nvidiaDeviceLookup struct {
	byUUID map[string]v1.StaticNodeAcceleratorDeviceStatus
	byID   map[string]v1.StaticNodeAcceleratorDeviceStatus
}

func newNvidiaDeviceLookup(devices []v1.StaticNodeAcceleratorDeviceStatus) nvidiaDeviceLookup {
	lookup := nvidiaDeviceLookup{
		byUUID: make(map[string]v1.StaticNodeAcceleratorDeviceStatus, len(devices)),
		byID:   make(map[string]v1.StaticNodeAcceleratorDeviceStatus, len(devices)),
	}

	for _, device := range devices {
		if device.UUID != "" {
			lookup.byUUID[device.UUID] = device
		}

		if device.ID != "" {
			lookup.byID[device.ID] = device
		}
	}

	return lookup
}

func nvidiaKubernetesAllocations(
	hardware adapter.HardwareSnapshot,
	evidence adapter.KubernetesEvidence,
) []v1.StaticNodeAllocationStatus {
	if !evidence.AllocationAvailable {
		return nil
	}

	pods := make(map[string]adapter.EndpointPodEvidence, len(evidence.EndpointPods))
	for _, pod := range evidence.EndpointPods {
		pods[pod.Namespace+"/"+pod.Name] = pod
	}

	lookup := newNvidiaDeviceLookup(hardware.Accelerator.Devices)
	allocations := make([]v1.StaticNodeAllocationStatus, 0, len(evidence.PodResources))

	for _, podResource := range evidence.PodResources {
		pod, ok := pods[podResource.Namespace+"/"+podResource.Name]
		if !ok || pod.Labels["endpoint"] == "" {
			continue
		}

		refs := make([]string, 0)
		for _, container := range podResource.Containers {
			refs = append(refs, container.DeviceIDs...)
		}

		devices := nvidiaAllocationDevices(refs, lookup, firstNonEmpty(evidence.Common.Labels.Node, pod.NodeName), 0)
		if len(devices) == 0 {
			continue
		}

		allocations = append(allocations, v1.StaticNodeAllocationStatus{
			WorkloadType: "endpoint",
			Workspace:    pod.Labels[v1.NeutreeClusterWorkspaceLabelKey],
			Endpoint:     pod.Labels["endpoint"],
			InstanceID:   podResource.Name,
			ReplicaID:    podResource.Name,
			RuntimeID:    podResource.Namespace + "/" + podResource.Name,
			Devices:      devices,
		})
	}

	sortAllocations(allocations)

	return allocations
}

func nvidiaStaticAllocations(
	hardware adapter.HardwareSnapshot,
	evidence adapter.StaticEvidence,
) []v1.StaticNodeAllocationStatus {
	if !evidence.AllocationAvailable {
		return nil
	}

	actors := make(map[string]adapter.RayActor, len(evidence.RayEvidence.Actors))
	for _, actor := range evidence.RayEvidence.Actors {
		actors[actor.ActorID] = actor
	}

	lookup := newNvidiaDeviceLookup(hardware.Accelerator.Devices)
	allocations := make([]v1.StaticNodeAllocationStatus, 0, len(evidence.RayEvidence.Replicas))

	for _, replica := range evidence.RayEvidence.Replicas {
		actor, ok := actors[replica.ActorID]
		if !ok || actor.PID <= 0 {
			continue
		}

		quantity, quantityKnown := nvidiaGPUQuantity(replica, actor)
		if quantityKnown && quantity <= 0 {
			continue
		}

		process, ok := evidence.RayEvidence.ActorProcesses[actor.PID]
		if !ok {
			continue
		}

		refs := nvidiaVisibleDeviceRefs(process.Environment, lookup)

		devices := nvidiaAllocationDevices(
			refs,
			lookup,
			firstNonEmpty(evidence.Common.Labels.Node, evidence.Common.Labels.NodeIP, replica.NodeID),
			quantity,
		)
		if len(devices) == 0 {
			continue
		}

		allocations = append(allocations, v1.StaticNodeAllocationStatus{
			WorkloadType: "endpoint",
			Workspace:    replica.Workspace,
			Endpoint:     replica.Endpoint,
			InstanceID:   replica.ActorID,
			ReplicaID:    firstNonEmpty(replica.ReplicaID, replica.ActorID),
			RuntimeID:    replica.ActorID,
			PID:          actor.PID,
			Devices:      devices,
		})
	}

	sortAllocations(allocations)

	return allocations
}

func nvidiaEndpointAllocations(
	labels adapter.CanonicalLabels,
	allocations []v1.StaticNodeAllocationStatus,
) []model.EndpointAllocation {
	result := make([]model.EndpointAllocation, 0, len(allocations))

	for _, allocation := range allocations {
		if allocation.WorkloadType != "" && allocation.WorkloadType != "endpoint" {
			continue
		}

		if allocation.Endpoint == "" || len(allocation.Devices) == 0 {
			continue
		}

		result = append(result, model.EndpointAllocation{
			Workspace:  firstNonEmpty(allocation.Workspace, labels.Workspace),
			Cluster:    labels.NeutreeCluster,
			Endpoint:   allocation.Endpoint,
			InstanceID: allocation.InstanceID,
			ReplicaID:  allocation.ReplicaID,
			NodeID:     firstNonEmpty(labels.Node, labels.NodeIP),
			Devices:    cloneDeviceAllocations(allocation.Devices),
		})
	}

	return result
}

func nvidiaAllocationDevices(
	references []string,
	lookup nvidiaDeviceLookup,
	nodeID string,
	quantity float64,
) []v1.DeviceAllocation {
	result := make([]v1.DeviceAllocation, 0, len(references))
	seen := make(map[string]struct{}, len(references))

	for _, reference := range references {
		device, ok := lookup.byUUID[reference]
		if !ok {
			device, ok = lookup.byID[reference]
		}

		if !ok || device.UUID == "" {
			continue
		}

		if _, exists := seen[device.UUID]; exists {
			continue
		}

		seen[device.UUID] = struct{}{}

		memoryMiB, coreUnits := nvidiaAllocationCapacity(device, quantity)
		result = append(result, v1.DeviceAllocation{
			UUID:      device.UUID,
			Product:   firstNonEmpty(device.ProductModel, device.ProductName),
			MemoryMiB: memoryMiB,
			CoreUnits: coreUnits,
			NodeID:    nodeID,
		})
	}

	return result
}

func nvidiaAllocationCapacity(device v1.StaticNodeAcceleratorDeviceStatus, quantity float64) (int64, int64) {
	if quantity > 0 && quantity < 1 {
		return int64(math.Round(float64(device.MemoryMiB) * quantity)), int64(math.Round(100 * quantity))
	}

	return device.MemoryMiB, 100
}

func nvidiaGPUQuantity(replica adapter.RayReplica, actor adapter.RayActor) (float64, bool) {
	if replica.GPUQuantity != 0 {
		return replica.GPUQuantity, true
	}

	for resource, quantity := range actor.RequiredResources {
		if strings.EqualFold(resource, "gpu") {
			return quantity, true
		}
	}

	return 0, false
}

func nvidiaVisibleDeviceRefs(environment map[string]string, lookup nvidiaDeviceLookup) []string {
	nvidiaVisible := strings.TrimSpace(environment["NVIDIA_VISIBLE_DEVICES"])
	if nvidiaVisibleContainsKnownUUIDs(nvidiaVisible, lookup) {
		return parseVisibleDeviceRefs(nvidiaVisible)
	}

	if cudaVisible := strings.TrimSpace(environment["CUDA_VISIBLE_DEVICES"]); cudaVisible != "" {
		return parseVisibleDeviceRefs(cudaVisible)
	}

	return parseVisibleDeviceRefs(nvidiaVisible)
}

func nvidiaVisibleContainsKnownUUIDs(value string, lookup nvidiaDeviceLookup) bool {
	refs := parseVisibleDeviceRefs(value)
	if len(refs) == 0 {
		return false
	}

	for _, reference := range refs {
		if _, ok := lookup.byUUID[reference]; !ok {
			return false
		}
	}

	return true
}

func parseVisibleDeviceRefs(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	switch strings.ToLower(value) {
	case "all", "none", "void", "no":
		return nil
	}

	result := make([]string, 0)

	for _, reference := range strings.Split(value, ",") {
		if reference = strings.TrimSpace(reference); reference != "" {
			result = append(result, reference)
		}
	}

	return result
}

func sortAllocations(allocations []v1.StaticNodeAllocationStatus) {
	sort.SliceStable(allocations, func(i, j int) bool {
		if allocations[i].Workspace != allocations[j].Workspace {
			return allocations[i].Workspace < allocations[j].Workspace
		}

		if allocations[i].Endpoint != allocations[j].Endpoint {
			return allocations[i].Endpoint < allocations[j].Endpoint
		}

		if allocations[i].InstanceID != allocations[j].InstanceID {
			return allocations[i].InstanceID < allocations[j].InstanceID
		}

		return allocations[i].RuntimeID < allocations[j].RuntimeID
	})
}

func cloneDeviceAllocations(devices []v1.DeviceAllocation) []v1.DeviceAllocation {
	result := make([]v1.DeviceAllocation, 0, len(devices))

	for _, device := range devices {
		copied := device

		if device.Order != nil {
			order := *device.Order
			copied.Order = &order
		}

		result = append(result, copied)
	}

	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}
