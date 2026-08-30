package nvidia

import (
	"cmp"
	"math"
	"sort"

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
			Workspace:  cmp.Or(allocation.Workspace, labels.Workspace),
			Cluster:    labels.NeutreeCluster,
			Endpoint:   allocation.Endpoint,
			InstanceID: allocation.InstanceID,
			ReplicaID:  allocation.ReplicaID,
			NodeID:     cmp.Or(labels.Node, labels.NodeIP),
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
			Product:   cmp.Or(device.ProductModel, device.ProductName),
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
