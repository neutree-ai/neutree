package neutreemetrics

import (
	"net/http"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const SourceNodeAgent = "neutree-node-agent"

type NodeSnapshot struct {
	Accelerator v1.StaticNodeAcceleratorStatus  `json:"accelerator,omitempty"`
	Allocations []v1.StaticNodeAllocationStatus `json:"allocations,omitempty"`
}

type SnapshotProvider interface {
	Snapshot(r *http.Request) (*NodeSnapshot, error)
}

type SnapshotProviderFunc func(r *http.Request) (*NodeSnapshot, error)

func (f SnapshotProviderFunc) Snapshot(r *http.Request) (*NodeSnapshot, error) {
	return f(r)
}

func snapshotFromAcceleratorMetrics(raw string) *NodeSnapshot {
	devices := acceleratorDevicesFromMetrics(raw)
	if len(devices) == 0 {
		cpu := v1.CPUStaticNodeAcceleratorStatus()

		return &NodeSnapshot{Accelerator: cpu}
	}

	return &NodeSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Type:         v1.AcceleratorTypeNVIDIAGPU.String(),
			Vendor:       "nvidia",
			ProductName:  "NVIDIA GPU",
			ProductModel: firstDeviceProductModel(devices, v1.AcceleratorTypeNVIDIAGPU.String()),
			Devices:      devices,
		},
	}
}

func acceleratorDevicesFromMetrics(raw string) []v1.StaticNodeAcceleratorDeviceStatus {
	parsed := parsePrometheusText(raw)
	devicesByUUID := map[string]v1.StaticNodeAcceleratorDeviceStatus{}

	for _, metric := range parsed {
		uuid := firstNonEmpty(metric.labels["UUID"], metric.labels["uuid"])
		if uuid == "" {
			continue
		}

		device := devicesByUUID[uuid]
		device.UUID = uuid
		device.Healthy = true

		if id := firstNonEmpty(metric.labels["gpu"], metric.labels["GPU_I_ID"]); id != "" {
			device.ID = id
		}
		if model := firstNonEmpty(metric.labels["modelName"], metric.labels["model"]); model != "" {
			device.ProductName = model
			device.ProductModel = model
		}

		if metric.name == "DCGM_FI_DEV_FB_TOTAL" && metric.value > 0 {
			device.MemoryMiB = int64(metric.value)
		}

		devicesByUUID[uuid] = device
	}

	devices := make([]v1.StaticNodeAcceleratorDeviceStatus, 0, len(devicesByUUID))
	for _, device := range devicesByUUID {
		devices = append(devices, device)
	}

	return devices
}

func firstDeviceProductModel(devices []v1.StaticNodeAcceleratorDeviceStatus, fallback string) string {
	for _, device := range devices {
		if device.ProductModel != "" {
			return device.ProductModel
		}
	}

	return fallback
}
