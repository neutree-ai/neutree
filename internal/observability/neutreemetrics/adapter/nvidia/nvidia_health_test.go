package nvidia

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestNvidiaAdapterEvaluatesHardwareHealth(t *testing.T) {
	provider, ok := New().(adapter.HardwareHealthProvider)
	require.True(t, ok)

	cases := []struct {
		name    string
		text    string
		up      bool
		healthy bool
	}{
		{
			name:    "one zero sample",
			text:    `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="PCIE"} 0`,
			up:      true,
			healthy: true,
		},
		{
			name: "exporter counter type",
			text: `# TYPE DCGM_EXP_GPU_HEALTH_STATUS counter
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 0`,
			up:      true,
			healthy: true,
		},
		{
			name: "all zero samples",
			text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="DRIVER"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="INFOROM"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="MCU"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="MEM"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="NVLINK"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="PCIE"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="PMU"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="POWER"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="SM"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="THERMAL"} 0`,
			up:      true,
			healthy: true,
		},
		{
			name: "nonzero followed by zero",
			text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="DRIVER"} 1
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="MCU"} 0`,
			up: true,
		},
		{
			name: "zero followed by nonzero",
			text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="DRIVER"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="MCU"} 2`,
			up: true,
		},
		{
			name: "ALL and later OK do not mask subsystem failure",
			text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="POWER",health_error_code="DCGM_FR_CLOCK_THROTTLE_POWER"} 1
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="POWER",health_error_code="DCGM_FR_OK"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL",health_error_code="DCGM_FR_OK"} 0`,
			up: true,
		},
		{
			name: "same subsystem with OK before failure",
			text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="MCU",health_error_code="DCGM_FR_OK"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="MCU",health_error_code="DCGM_FR_UNKNOWN"} 2`,
			up: true,
		},
		{
			name: "new subsystem is not excluded",
			text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="FUTURE_SUBSYSTEM"} 1`,
			up: true,
		},
		{
			name: "other device failure does not affect current device",
			text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="POWER"} 1
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="POWER"} 0`,
			up:      true,
			healthy: true,
		},
		{
			name: "other device zero does not match",
			text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",gpu="0",health_watch="ALL"} 0`,
			up:   true,
		},
		{name: "missing metric", text: `DCGM_FI_DEV_GPU_UTIL{UUID="GPU-abc"} 0`, up: true},
		{name: "missing UUID", text: `DCGM_EXP_GPU_HEALTH_STATUS{gpu="0",health_watch="ALL"} 0`, up: true},
		{name: "missing health_watch", text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc"} 0`, up: true},
		{name: "empty health_watch", text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch=""} 0`, up: true},
		{name: "empty metrics", up: true},
		{name: "invalid value", text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} invalid`, up: true},
		{name: "NaN value", text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} NaN`, up: true},
		{name: "positive infinity", text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} +Inf`, up: true},
		{name: "negative infinity", text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} -Inf`, up: true},
		{name: "negative status", text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} -1`, up: true},
		{name: "exporter down", text: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 0`},
		{name: "lowercase UUID label", text: `DCGM_EXP_GPU_HEALTH_STATUS{uuid="GPU-abc",health_watch="ALL"} 0`, up: true, healthy: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, initialHealth := range []bool{false, true} {
				hardware := adapter.HardwareSnapshot{Accelerator: v1.StaticNodeAcceleratorStatus{
					Type: v1.AcceleratorTypeNVIDIAGPU.String(),
					Devices: []v1.StaticNodeAcceleratorDeviceStatus{
						{ID: "0", UUID: "GPU-abc", MemoryMiB: 81920, Healthy: initialHealth},
						{ID: "1", UUID: "GPU-missing", Healthy: initialHealth},
						{ID: "2", Healthy: initialHealth},
					},
				}}
				original := hardware.Clone()

				result := provider.EvaluateHardwareHealth(hardware, adapter.CommonEvidence{
					ExporterText: testCase.text,
					ExporterUp:   testCase.up,
				})

				require.Len(t, result.Accelerator.Devices, 3)
				assert.Equal(t, testCase.healthy, result.Accelerator.Devices[0].Healthy)
				assert.False(t, result.Accelerator.Devices[1].Healthy)
				assert.False(t, result.Accelerator.Devices[2].Healthy)
				assert.Equal(t, original, hardware)
				assert.Equal(t, original.Accelerator.Type, result.Accelerator.Type)
				assert.Equal(t, original.Accelerator.Devices[0].MemoryMiB, result.Accelerator.Devices[0].MemoryMiB)
			}
		})
	}
}

func TestNvidiaAdapterReportsOnlyHealthyDevicesAsFree(t *testing.T) {
	cases := []struct {
		name       string
		healthText string
		free       float64
	}{
		{
			name: "all healthy",
			healthText: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="ALL"} 0`,
			free: 2,
		},
		{
			name: "one unhealthy",
			healthText: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 1
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="ALL"} 0`,
			free: 1,
		},
		{
			name: "all unhealthy",
			healthText: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 1
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="ALL"} 2`,
		},
		{
			name: "subsystem failure alongside healthy ALL and duplicate POWER",
			healthText: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL",health_error_code="DCGM_FR_OK"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="POWER",health_error_code="DCGM_FR_CLOCK_THROTTLE_POWER"} 1
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="POWER",health_error_code="DCGM_FR_OK"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="ALL",health_error_code="DCGM_FR_OK"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="MCU",health_error_code="DCGM_FR_OK"} 0`,
			free: 1,
		},
		{name: "missing health metrics"},
		{name: "other device health", healthText: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-other",health_watch="ALL"} 0`},
		{name: "one missing health", healthText: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 0`, free: 1},
	}

	for _, clusterType := range []string{v1.SSHClusterType, v1.KubernetesClusterType} {
		for _, testCase := range cases {
			t.Run(clusterType+"/"+testCase.name, func(t *testing.T) {
				accelerator := &accelerator{}
				evidence := adapter.CommonEvidence{
					ExporterUp: true,
					ExporterText: nvidiaDCGMFixture("GPU-abc", "A100", "0", 0, 0, 81920) +
						nvidiaDCGMFixture("GPU-def", "A100", "1", 0, 0, 81920) + testCase.healthText,
					Labels: adapter.CanonicalLabels{ClusterType: clusterType, Node: "node-a"},
				}
				hardware := accelerator.EvaluateHardwareHealth(adapter.HardwareSnapshot{
					Accelerator: v1.StaticNodeAcceleratorStatus{
						Type: v1.AcceleratorTypeNVIDIAGPU.String(),
						Devices: []v1.StaticNodeAcceleratorDeviceStatus{
							{UUID: "GPU-abc", ProductModel: "A100", Healthy: true},
							{UUID: "GPU-def", ProductModel: "A100", Healthy: true},
						},
					},
				}, evidence)

				var result adapter.MetricResult
				var err error
				if clusterType == v1.SSHClusterType {
					result, err = accelerator.BuildStaticMetrics(context.Background(), hardware, adapter.StaticEvidence{Common: evidence})
				} else {
					result, err = accelerator.BuildKubernetesMetrics(context.Background(), hardware, adapter.KubernetesEvidence{Common: evidence})
				}
				require.NoError(t, err)
				assert.Equal(t, testCase.free, requireNvidiaSample(t, result.Samples, "neutree_node_accelerator_free", nil).Value)
				assert.Equal(t, 2.0, requireNvidiaSample(t, result.Samples, "neutree_node_accelerator_total", nil).Value)
				assert.Zero(t, requireNvidiaSample(t, result.Samples, "neutree_node_accelerator_allocated", nil).Value)
			})
		}
	}
}

func TestNvidiaNodeGPUFreeMetricsExcludeAllocationsAndUnhealthyDevices(t *testing.T) {
	raw := nvidiaDCGMFixture("GPU-abc", "A100", "0", 0, 0, 81920) +
		nvidiaDCGMFixture("GPU-def", "A100", "1", 0, 0, 81920) +
		`DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="ALL"} 1`

	for _, allocatedUUID := range []string{"GPU-abc", "GPU-def"} {
		t.Run(allocatedUUID, func(t *testing.T) {
			samples := nvidiaNormalizeNodeGPUSamples(adapter.CanonicalLabels{}, raw, []model.EndpointAllocation{
				nvidiaTestAllocation("chat", "replica-a", allocatedUUID, "A100", 81920),
			})
			free := 0.0
			if allocatedUUID == "GPU-def" {
				free = 1
			}
			assert.Equal(t, free, requireNvidiaSample(t, samples, "neutree_node_accelerator_free", nil).Value)
			assert.Equal(t, 2.0, requireNvidiaSample(t, samples, "neutree_node_accelerator_total", nil).Value)
			assert.Equal(t, 1.0, requireNvidiaSample(t, samples, "neutree_node_accelerator_allocated", nil).Value)
		})
	}
}
