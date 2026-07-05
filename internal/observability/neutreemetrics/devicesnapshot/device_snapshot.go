package devicesnapshot

import (
	"strconv"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
)

func firstNonEmpty(values ...string) string {
	return model.FirstNonEmpty(values...)
}

func FromAcceleratorMetrics(raw string) *model.NodeDeviceSnapshot {
	devices := acceleratorDevicesFromMetrics(raw)
	if len(devices) == 0 {
		cpu := v1.CPUStaticNodeAcceleratorStatus()

		return &model.NodeDeviceSnapshot{Accelerator: cpu}
	}

	return &model.NodeDeviceSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Type:    v1.AcceleratorTypeNVIDIAGPU.String(),
			Devices: devices,
		},
	}
}

func acceleratorDevicesFromMetrics(raw string) []v1.StaticNodeAcceleratorDeviceStatus {
	parsed := promtext.ParseVector(raw)
	devicesByUUID := map[string]v1.StaticNodeAcceleratorDeviceStatus{}
	discoveredUUIDs := map[string]struct{}{}

	for _, metric := range parsed {
		uuid := promtext.LabelValue(metric, "UUID", "uuid")
		if uuid == "" {
			continue
		}

		if promtext.MetricName(metric) == "DCGM_FI_DEV_GPU_UTIL" {
			discoveredUUIDs[uuid] = struct{}{}
		}

		device := devicesByUUID[uuid]
		if device.UUID == "" {
			device.MinorNumber = v1.StaticNodeAcceleratorDeviceMinorNumberUnknown
		}
		device.UUID = uuid
		device.Healthy = true

		if id := promtext.LabelValue(metric, "gpu", "GPU_I_ID"); id != "" {
			device.ID = id
			if minorNumber, err := strconv.Atoi(id); err == nil {
				device.MinorNumber = minorNumber
			}
		}

		if model := promtext.LabelValue(metric, "modelName", "model"); model != "" {
			device.ProductName = model
			device.ProductModel = model
		}

		if promtext.MetricName(metric) == "DCGM_FI_DEV_FB_TOTAL" && promtext.Value(metric) > 0 {
			device.MemoryMiB = int64(promtext.Value(metric))
		}

		devicesByUUID[uuid] = device
	}

	devices := make([]v1.StaticNodeAcceleratorDeviceStatus, 0, len(devicesByUUID))
	for _, device := range devicesByUUID {
		if _, ok := discoveredUUIDs[device.UUID]; !ok {
			continue
		}

		devices = append(devices, device)
	}

	return devices
}
