package nvidia

import (
	"math"
	"sort"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

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
	hardwareSnapshot adapter.HardwareSnapshot,
	evidence adapter.KubernetesEvidence,
) ([]v1.StaticNodeAllocationStatus, error) {
	if !evidence.AllocationAvailable {
		return nil, nil
	}

	podResources := make(map[string]adapter.PodResource, len(evidence.PodResources))
	for _, podResource := range evidence.PodResources {
		podResources[podResource.Namespace+"/"+podResource.Name] = podResource
	}

	lookup := newNvidiaDeviceLookup(hardwareSnapshot.Accelerator.Devices)

	products, err := nvidiaHAMiDeviceProducts(evidence, hardwareSnapshot)
	if err != nil {
		return nil, err
	}

	allocations := make([]v1.StaticNodeAllocationStatus, 0, len(evidence.EndpointPods))

	for _, pod := range evidence.EndpointPods {
		if pod.Labels["endpoint"] == "" {
			continue
		}

		nodeID := firstNonEmpty(evidence.Common.Labels.Node, pod.NodeName)

		devices, err := nvidiaHAMiDeviceAllocations(
			pod.Annotations[nvidiaHAMiVGPUDevicesAllocated],
			nodeID,
			products,
		)
		if err != nil {
			return nil, err
		}

		if len(devices) == 0 {
			podResource, ok := podResources[pod.Namespace+"/"+pod.Name]
			if !ok {
				continue
			}

			refs := make([]string, 0)
			for _, container := range podResource.Containers {
				refs = append(refs, container.DeviceIDs...)
			}

			devices = nvidiaAllocationDevices(refs, lookup, nodeID, 0)
		}

		if len(devices) == 0 {
			continue
		}

		allocations = append(allocations, v1.StaticNodeAllocationStatus{
			WorkloadType: "endpoint",
			Workspace:    pod.Labels[v1.NeutreeClusterWorkspaceLabelKey],
			Endpoint:     pod.Labels["endpoint"],
			InstanceID:   pod.Name,
			ReplicaID:    pod.Name,
			RuntimeID:    pod.Namespace + "/" + pod.Name,
			Devices:      devices,
		})
	}

	sortAllocations(allocations)

	return allocations, nil
}

func nvidiaStaticAllocations(
	hardwareSnapshot adapter.HardwareSnapshot,
	evidence adapter.StaticEvidence,
) []v1.StaticNodeAllocationStatus {
	if !evidence.AllocationAvailable {
		return nil
	}

	actors := make(map[string]adapter.RayActor, len(evidence.RayEvidence.Actors))
	for _, actor := range evidence.RayEvidence.Actors {
		actors[actor.ActorID] = actor
	}

	lookup := newNvidiaDeviceLookup(hardwareSnapshot.Accelerator.Devices)
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

		refs := nvidiaStaticDeviceRefs(process, evidence.RayEvidence.AcceleratorProcesses, lookup)

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

func nvidiaStaticDeviceRefs(
	process adapter.ProcessInfo,
	acceleratorProcesses []adapter.AcceleratorProcess,
	lookup nvidiaDeviceLookup,
) []string {
	visibleRefs := nvidiaVisibleDeviceRefs(process.Environment, lookup)
	if len(visibleRefs) > 0 {
		return visibleRefs
	}

	return nvidiaProcessDeviceRefs(process, acceleratorProcesses, lookup)
}

func nvidiaProcessDeviceRefs(
	process adapter.ProcessInfo,
	acceleratorProcesses []adapter.AcceleratorProcess,
	lookup nvidiaDeviceLookup,
) []string {
	pids := nvidiaProcessPIDSet(process)
	if len(pids) == 0 {
		return nil
	}

	seen := map[string]struct{}{}
	references := make([]string, 0)

	for _, acceleratorProcess := range acceleratorProcesses {
		if _, ok := pids[acceleratorProcess.PID]; !ok {
			continue
		}

		if _, ok := lookup.byUUID[acceleratorProcess.DeviceID]; !ok {
			continue
		}

		if _, ok := seen[acceleratorProcess.DeviceID]; ok {
			continue
		}

		seen[acceleratorProcess.DeviceID] = struct{}{}

		references = append(references, acceleratorProcess.DeviceID)
	}

	sort.Strings(references)

	return references
}

func nvidiaProcessPIDSet(process adapter.ProcessInfo) map[int]struct{} {
	pids := make(map[int]struct{}, len(process.DescendantPIDs)+1)
	if process.PID > 0 {
		pids[process.PID] = struct{}{}
	}

	for _, pid := range process.DescendantPIDs {
		if pid > 0 {
			pids[pid] = struct{}{}
		}
	}

	return pids
}

func nvidiaStaticEndpointReplicaGPUUsages(
	labels adapter.CanonicalLabels,
	evidence adapter.StaticEvidence,
	allocations []v1.StaticNodeAllocationStatus,
) []model.EndpointReplicaGPUUsage {
	usages := make([]model.EndpointReplicaGPUUsage, 0)

	for _, allocation := range allocations {
		process, ok := evidence.RayEvidence.ActorProcesses[allocation.PID]
		if !ok {
			continue
		}

		pids := nvidiaProcessPIDSet(process)
		for _, device := range allocation.Devices {
			memoryUsedBytes, ok := nvidiaProcessMemoryUsedBytes(
				device.UUID,
				pids,
				evidence.RayEvidence.AcceleratorProcesses,
			)
			if !ok {
				continue
			}

			usages = append(usages, model.EndpointReplicaGPUUsage{
				Workspace:       firstNonEmpty(allocation.Workspace, labels.Workspace),
				Cluster:         labels.NeutreeCluster,
				Endpoint:        allocation.Endpoint,
				InstanceID:      allocation.InstanceID,
				ReplicaID:       allocation.ReplicaID,
				NodeID:          firstNonEmpty(device.NodeID, labels.Node, labels.NodeIP),
				GPUUUID:         device.UUID,
				AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
				VDeviceIndex:    device.VDeviceIndex,
				Product:         device.Product,
				MemoryUsedBytes: &memoryUsedBytes,
			})
		}
	}

	return usages
}

func nvidiaProcessMemoryUsedBytes(
	deviceUUID string,
	pids map[int]struct{},
	acceleratorProcesses []adapter.AcceleratorProcess,
) (float64, bool) {
	seenPIDs := map[int]struct{}{}
	var memoryUsedBytes float64
	hasMemory := false

	for _, process := range acceleratorProcesses {
		if process.DeviceID != deviceUUID || process.MemoryUsedBytes == nil {
			continue
		}

		if _, ok := pids[process.PID]; !ok {
			continue
		}

		if _, ok := seenPIDs[process.PID]; ok {
			continue
		}

		seenPIDs[process.PID] = struct{}{}
		memoryUsedBytes += *process.MemoryUsedBytes
		hasMemory = true
	}

	return memoryUsedBytes, hasMemory
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
	for resource, quantity := range actor.RequiredResources {
		if strings.EqualFold(resource, "gpu") {
			return quantity, true
		}
	}

	return nvidiaDeploymentGPUQuantity(replica.DeploymentOptions)
}

func nvidiaDeploymentGPUQuantity(options map[string]interface{}) (float64, bool) {
	if len(options) == 0 {
		return 0, false
	}

	return nvidiaNumberAsFloat64(options["num_gpus"])
}

func nvidiaNumberAsFloat64(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	default:
		return 0, false
	}
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
