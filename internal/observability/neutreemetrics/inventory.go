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
	fresh = fresh.Clone()

	if _, hasHealthProvider := accel.(adapter.HardwareHealthProvider); hasHealthProvider {
		previousHealth := make(map[string]bool, len(s.inventory.Accelerator.Devices))
		for _, device := range s.inventory.Accelerator.Devices {
			previousHealth[device.UUID] = device.Healthy
		}

		discoveredHealth := make(map[string]bool, len(fresh.Accelerator.Devices))
		for i, device := range fresh.Accelerator.Devices {
			discoveredHealth[device.UUID] = device.Healthy

			if healthy, found := previousHealth[device.UUID]; found {
				fresh.Accelerator.Devices[i].Healthy = device.Healthy && healthy
			}
		}

		fresh = s.evaluateAdapterHardwareHealth(accel, fresh, exporter)

		// Exporter health can clear cached failures, but not this cycle's hardware failure.
		for i, device := range fresh.Accelerator.Devices {
			fresh.Accelerator.Devices[i].Healthy = device.Healthy && discoveredHealth[device.UUID]
		}
	}

	s.inventory = mergeHardwareInventory(s.inventory, fresh, s.config.AcceleratorType)

	if err != nil {
		klog.ErrorS(err, "Accelerator discovery failed; retaining the process-local device inventory",
			"accelerator_type", s.config.AcceleratorType, "known_devices", len(s.inventory.Accelerator.Devices))
	}

	return s.inventory.Clone(), err
}

func mergeHardwareInventory(previous, fresh adapter.HardwareSnapshot, acceleratorType string) adapter.HardwareSnapshot {
	result := previous.Clone()
	fresh = fresh.Clone()
	result.Accelerator.Type = acceleratorType
	positions := make(map[string]int, len(result.Accelerator.Devices))

	for i := range result.Accelerator.Devices {
		positions[result.Accelerator.Devices[i].UUID] = i
		result.Accelerator.Devices[i].Healthy = false
	}

	seen := make(map[string]bool, len(fresh.Accelerator.Devices))

	for _, device := range fresh.Accelerator.Devices {
		if device.UUID == "" {
			continue
		}

		seen[device.UUID] = true

		if index, found := positions[device.UUID]; found {
			result.Accelerator.Devices[index] = retainDeviceIdentity(result.Accelerator.Devices[index], device)
			continue
		}

		positions[device.UUID] = len(result.Accelerator.Devices)
		result.Accelerator.Devices = append(result.Accelerator.Devices, device)
	}

	for _, device := range result.Accelerator.Devices {
		if !seen[device.UUID] {
			klog.Warningf("Previously discovered accelerator is inaccessible and may be lost: type=%q UUID=%q index=%q",
				acceleratorType, device.UUID, device.ID)
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
