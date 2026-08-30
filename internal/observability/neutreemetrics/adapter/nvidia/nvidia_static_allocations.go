package nvidia

import (
	"cmp"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// nvidiaStaticAllocations uses Ray resource requests, process environments,
// and NVIDIA process observations to resolve static-cluster allocations.
func nvidiaStaticAllocations(
	hardwareSnapshot adapter.HardwareSnapshot,
	evidence adapter.StaticEvidence,
	gpuProcesses []nvidiaGPUProcess,
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

		nodeID := cmp.Or(evidence.Common.Labels.Node, evidence.Common.Labels.NodeIP, replica.NodeID)
		refs := nvidiaVisibleDeviceRefs(process.Environment, lookup)
		devices := nvidiaAllocationDevices(refs, lookup, nodeID, quantity)
		processDevices := nvidiaAllocationDevicesFromGPUProcesses(
			process,
			gpuProcesses,
			lookup,
			nodeID,
			quantity,
		)

		if len(processDevices) > 0 {
			devices = mergeNvidiaAllocationDeviceUsage(devices, processDevices)
		}

		if len(devices) == 0 {
			continue
		}

		allocations = append(allocations, v1.StaticNodeAllocationStatus{
			WorkloadType: "endpoint",
			Workspace:    replica.Workspace,
			Endpoint:     replica.Endpoint,
			InstanceID:   replica.ActorID,
			ReplicaID:    cmp.Or(replica.ReplicaID, replica.ActorID),
			RuntimeID:    replica.ActorID,
			PID:          actor.PID,
			Devices:      devices,
		})
	}

	sortAllocations(allocations)

	return allocations
}

func nvidiaAllocationDevicesFromGPUProcesses(
	process adapter.ProcessInfo,
	gpuProcesses []nvidiaGPUProcess,
	lookup nvidiaDeviceLookup,
	nodeID string,
	quantity float64,
) []v1.DeviceAllocation {
	pids := make(map[int]struct{}, len(process.DescendantPIDs)+1)
	if process.PID > 0 {
		pids[process.PID] = struct{}{}
	}

	for _, pid := range process.DescendantPIDs {
		if pid > 0 {
			pids[pid] = struct{}{}
		}
	}

	refs := make([]string, 0, len(gpuProcesses))
	usedMemoryMiBByUUID := make(map[string]int64, len(gpuProcesses))

	for _, gpuProcess := range gpuProcesses {
		if _, ok := pids[gpuProcess.PID]; !ok || gpuProcess.UUID == "" {
			continue
		}

		refs = append(refs, gpuProcess.UUID)
		usedMemoryMiBByUUID[gpuProcess.UUID] += gpuProcess.UsedMemoryMiB
	}

	devices := nvidiaAllocationDevices(refs, lookup, nodeID, quantity)
	for index := range devices {
		devices[index].UsedMemoryMiB = usedMemoryMiBByUUID[devices[index].UUID]
	}

	return devices
}

func mergeNvidiaAllocationDeviceUsage(
	allocatedDevices []v1.DeviceAllocation,
	processDevices []v1.DeviceAllocation,
) []v1.DeviceAllocation {
	if len(allocatedDevices) == 0 {
		return processDevices
	}

	usedMemoryMiBByUUID := make(map[string]int64, len(processDevices))

	for _, device := range processDevices {
		if device.UUID != "" {
			usedMemoryMiBByUUID[device.UUID] += device.UsedMemoryMiB
		}
	}

	for index := range allocatedDevices {
		if allocatedDevices[index].UUID != "" {
			allocatedDevices[index].UsedMemoryMiB = usedMemoryMiBByUUID[allocatedDevices[index].UUID]
		}
	}

	return allocatedDevices
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
