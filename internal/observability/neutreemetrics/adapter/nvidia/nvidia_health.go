package nvidia

import (
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

const nvidiaDCGMDeviceHealthMetric = "DCGM_EXP_GPU_HEALTH_STATUS"

func (*accelerator) EvaluateHardwareHealth(hardware adapter.HardwareSnapshot, evidence adapter.CommonEvidence) adapter.HardwareSnapshot {
	var HealthStatusByUUID map[string]bool
	if evidence.ExporterUp {
		HealthStatusByUUID = nvidiaHealthByUUID(evidence.ExporterText)
	}

	result := hardware.Clone()
	for deviceIndex, device := range result.Accelerator.Devices {
		result.Accelerator.Devices[deviceIndex].Healthy = HealthStatusByUUID[device.UUID]
	}

	return result
}

func nvidiaHealthByUUID(raw string) map[string]bool {
	HealthStatusByUUID := make(map[string]bool)

	for _, sample := range promtext.ParseVector(raw) {
		if promtext.MetricName(sample) != nvidiaDCGMDeviceHealthMetric {
			continue
		}

		uuid := promtext.LabelValue(sample, "UUID", "uuid")
		if uuid == "" || promtext.LabelValue(sample, "health_watch") == "" {
			continue
		}

		if _, found := HealthStatusByUUID[uuid]; !found {
			HealthStatusByUUID[uuid] = true
		}

		if promtext.Value(sample) != 0 {
			HealthStatusByUUID[uuid] = false
		}
	}

	return HealthStatusByUUID
}
