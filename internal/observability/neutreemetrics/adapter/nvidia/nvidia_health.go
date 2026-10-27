package nvidia

import (
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// nvidiaDCGMDeviceHealthMetric is DCGM Exporter's per-GPU health gauge:
// 0 = PASS, 10 = WARN, 20 = FAIL. health_watch identifies a subsystem or ALL;
// health_error_code, health_error_severity, and health_error_category describe errors.
// Only a FAIL health_watch series makes a GPU unhealthy; WARN is logged.
// See https://docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html#gpu-health-status.
const nvidiaDCGMDeviceHealthMetric = "DCGM_EXP_GPU_HEALTH_STATUS"
const nvidiaDCGMDeviceHealthFail = 20

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
		healthWatch := promtext.LabelValue(sample, "health_watch")
		healthStatus := promtext.Value(sample)

		if uuid == "" || healthWatch == "" {
			klog.Warningf("Skipping NVIDIA GPU health metric with missing labels: UUID=%q, health_watch=%q, value=%v", uuid, healthWatch, healthStatus)
			continue
		}

		if healthStatus != 0 {
			klog.Warningf("NVIDIA GPU health metric is nonzero: UUID=%q, health_watch=%q, health_error_code=%q, value=%v",
				uuid, healthWatch, promtext.LabelValue(sample, "health_error_code"), healthStatus)
		}

		if _, found := HealthStatusByUUID[uuid]; !found {
			HealthStatusByUUID[uuid] = true
		}

		if healthStatus == nvidiaDCGMDeviceHealthFail {
			HealthStatusByUUID[uuid] = false
		}
	}

	return HealthStatusByUUID
}
