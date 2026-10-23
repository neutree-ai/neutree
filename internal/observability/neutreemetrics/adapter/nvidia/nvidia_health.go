package nvidia

import (
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// nvidiaDCGMDeviceHealthMetric is DCGM Exporter's per-GPU health gauge:
// 0 = PASS, 10 = WARN, 20 = FAIL. health_watch identifies a subsystem or ALL;
// health_error_code, health_error_severity, and health_error_category describe errors.
// Any nonzero health_watch series for a GPU makes that GPU unhealthy.
// See https://docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html#gpu-health-status.
const nvidiaDCGMDeviceHealthMetric = "DCGM_EXP_GPU_HEALTH_STATUS"

func (*accelerator) EvaluateHardwareHealth(hardware adapter.HardwareSnapshot, evidence adapter.CommonEvidence) adapter.HardwareSnapshot {
	var HealthStatusByUUID map[string]bool
	if evidence.ExporterUp {
		HealthStatusByUUID = nvidiaHealthByUUID(evidence.ExporterText)
	}

	result := hardware.Clone()
	for deviceIndex, device := range result.Accelerator.Devices {
		if healthy, found := HealthStatusByUUID[device.UUID]; found {
			result.Accelerator.Devices[deviceIndex].Healthy = healthy
		}
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
