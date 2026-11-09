package neutreemetrics

import (
	"context"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// observeAdapterHardware shares the process-local baseline across metrics and
// device snapshots. Missing devices retain their identity but cannot be revived
// by stale exporter health, which is evaluated only on this cycle's discovery.
func (s *Server) observeAdapterHardware(
	ctx context.Context,
	accel adapter.Accelerator,
	exporter *model.ScrapeResult,
) (adapter.HardwareSnapshot, error) {
	s.inventoryMu.Lock()
	defer s.inventoryMu.Unlock()

	fresh, err := s.discoverAdapterHardware(ctx, accel)
	if err != nil {
		klog.ErrorS(err, "Accelerator discovery failed; retaining the process-local device inventory",
			"accelerator_type", s.config.AcceleratorType, "known_devices", len(s.inventory.Accelerator.Devices))
	}

	if fresh.Accelerator.Type == v1.StaticNodeAcceleratorTypeCPU && len(s.inventory.Accelerator.Devices) == 0 {
		return fresh, err
	}

	fresh = s.evaluateAdapterHardwareHealth(accel, fresh, exporter)

	s.inventory = mergeHardwareInventory(s.inventory, fresh, s.config.AcceleratorType)

	// Callers use the result after the inventory mutex is released.
	return s.inventory.Clone(), err
}

func mergeHardwareInventory(previous, fresh adapter.HardwareSnapshot, acceleratorType string) adapter.HardwareSnapshot {
	result := previous
	result.Accelerator.Type = acceleratorType
	positions := make(map[string]int, len(fresh.Accelerator.Devices))

	for index, device := range fresh.Accelerator.Devices {
		if device.UUID != "" {
			positions[device.UUID] = index
		}
	}

	for index, device := range result.Accelerator.Devices {
		if freshIndex, found := positions[device.UUID]; found {
			result.Accelerator.Devices[index] = retainDeviceIdentity(device, fresh.Accelerator.Devices[freshIndex])
			delete(positions, device.UUID)

			continue
		}

		result.Accelerator.Devices[index].Healthy = false

		klog.Warningf("Previously discovered accelerator is inaccessible and may be lost: type=%q UUID=%q index=%q",
			acceleratorType, device.UUID, device.ID)
	}

	for index, device := range fresh.Accelerator.Devices {
		if _, found := positions[device.UUID]; found {
			result.Accelerator.Devices = append(result.Accelerator.Devices, fresh.Accelerator.Devices[index])

			delete(positions, device.UUID)
		}
	}

	result.Details = mergeHardwareDetails(result.Details, fresh.Details)

	return result
}

func mergeHardwareDetails(previous, fresh []adapter.HardwareDetails) []adapter.HardwareDetails {
	positions := make(map[string]int, len(previous))
	for i, detail := range previous {
		positions[detail.UUID] = i
	}

	for _, detail := range fresh {
		if index, found := positions[detail.UUID]; found {
			previous[index] = detail
			continue
		}

		positions[detail.UUID] = len(previous)
		previous = append(previous, detail)
	}

	return previous
}

func retainDeviceIdentity(previous, fresh v1.StaticNodeAcceleratorDeviceStatus) v1.StaticNodeAcceleratorDeviceStatus {
	if fresh.ID == "" {
		fresh.ID = previous.ID
	}

	if fresh.MinorNumber == nil {
		fresh.MinorNumber = previous.MinorNumber
	}

	if fresh.ProductName == "" {
		fresh.ProductName = previous.ProductName
	}

	if fresh.ProductModel == "" {
		fresh.ProductModel = previous.ProductModel
	}

	if fresh.MemoryMiB == 0 {
		fresh.MemoryMiB = previous.MemoryMiB
	}

	return fresh
}
