package neutreemetrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/adapter/nvidia"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

type healthSnapshotAccelerator struct {
	adapter.Accelerator
	adapter.HardwareHealthProvider
	hardware        adapter.HardwareSnapshot
	metricsHardware adapter.HardwareSnapshot
}

func (accelerator *healthSnapshotAccelerator) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	return accelerator.hardware, nil
}

func (accelerator *healthSnapshotAccelerator) BuildStaticMetrics(
	_ context.Context,
	hardware adapter.HardwareSnapshot,
	_ adapter.StaticEvidence,
) (adapter.MetricResult, error) {
	accelerator.metricsHardware = hardware.Clone()
	return adapter.MetricResult{}, nil
}

func (accelerator *healthSnapshotAccelerator) BuildKubernetesMetrics(
	_ context.Context,
	hardware adapter.HardwareSnapshot,
	_ adapter.KubernetesEvidence,
) (adapter.MetricResult, error) {
	accelerator.metricsHardware = hardware.Clone()
	return adapter.MetricResult{}, nil
}

func TestServerEvaluatesHardwareHealthBeforeSnapshotsAndMetrics(t *testing.T) {
	provider, ok := nvidia.New().(adapter.HardwareHealthProvider)
	require.True(t, ok)
	cases := []struct {
		name   string
		body   string
		status int
		health []bool
	}{
		{
			name: "healthy and unhealthy devices",
			body: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="ALL"} 1`,
			status: http.StatusOK,
			health: []bool{true, false, false},
		},
		{
			name: "power failure followed by OK remains unhealthy",
			body: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="DRIVER"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="MCU"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="POWER",health_error_code="DCGM_FR_POWER_UNREADABLE"} 1
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="POWER",health_error_code="DCGM_FR_OK"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="ALL"} 0`,
			status: http.StatusOK,
			health: []bool{false, true, false},
		},
		{
			name: "power OK followed by failure remains unhealthy",
			body: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="DRIVER"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="MCU"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="POWER",health_error_code="DCGM_FR_OK"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="POWER",health_error_code="DCGM_FR_POWER_UNREADABLE"} 1
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="ALL"} 0`,
			status: http.StatusOK,
			health: []bool{false, true, false},
		},
		{
			name: "missing health watch fails closed",
			body: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc"} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="ALL"} 0`,
			status: http.StatusOK,
			health: []bool{false, true, false},
		},
		{
			name: "empty health watch fails closed",
			body: `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch=""} 0
DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-def",health_watch="ALL"} 0`,
			status: http.StatusOK,
			health: []bool{false, true, false},
		},
		{
			name:   "missing health metrics",
			body:   `DCGM_FI_DEV_GPU_UTIL{UUID="GPU-abc"} 0`,
			status: http.StatusOK,
			health: []bool{false, false, false},
		},
		{
			name:   "exporter unavailable",
			body:   `DCGM_EXP_GPU_HEALTH_STATUS{UUID="GPU-abc",health_watch="ALL"} 0`,
			status: http.StatusServiceUnavailable,
			health: []bool{false, false, false},
		},
		{
			name:   "no exporter configured",
			health: []bool{false, false, false},
		},
	}

	for _, clusterType := range []string{v1.SSHClusterType, v1.KubernetesClusterType} {
		for _, testCase := range cases {
			t.Run(clusterType+"/"+testCase.name, func(t *testing.T) {
				accelerator := &healthSnapshotAccelerator{
					Accelerator:            nvidia.New(),
					HardwareHealthProvider: provider,
					hardware: adapter.HardwareSnapshot{Accelerator: v1.StaticNodeAcceleratorStatus{
						Type: v1.AcceleratorTypeNVIDIAGPU.String(),
						Devices: []v1.StaticNodeAcceleratorDeviceStatus{
							{UUID: "GPU-abc", Healthy: true},
							{UUID: "GPU-def", Healthy: true},
							{UUID: "GPU-missing", Healthy: true},
						},
					}},
				}
				original := accelerator.hardware.Clone()
				config := Config{
					ClusterType:     clusterType,
					AcceleratorType: accelerator.Type(),
					Accelerators:    map[string]adapter.Accelerator{accelerator.Type(): accelerator},
				}
				var scrape *model.ScrapeResult
				if testCase.status != 0 {
					exporter := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
						writer.WriteHeader(testCase.status)
						_, _ = writer.Write([]byte(testCase.body))
					}))
					t.Cleanup(exporter.Close)
					config.ScrapeTargetProvider = testTargetProvider("", exporter.URL)
					scrape = &model.ScrapeResult{Body: testCase.body, Up: testCase.status == http.StatusOK}
				}
				server, err := NewServer(config)
				require.NoError(t, err)

				response := httptest.NewRecorder()
				server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/node/device-snapshot", nil))
				require.Equal(t, http.StatusOK, response.Code)
				var snapshot v1.NodeDeviceSnapshot
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &snapshot))
				require.Len(t, snapshot.Accelerator.Devices, len(testCase.health))
				for deviceIndex, healthy := range testCase.health {
					assert.Equal(t, healthy, snapshot.Accelerator.Devices[deviceIndex].Healthy)
					assert.Equal(t, healthy, accelerator.metricsHardware.Accelerator.Devices[deviceIndex].Healthy)
				}

				server.acceleratorSamples(context.Background(), accelerator, scrape)
				for deviceIndex, healthy := range testCase.health {
					assert.Equal(t, healthy, accelerator.metricsHardware.Accelerator.Devices[deviceIndex].Healthy)
				}
				assert.Equal(t, original, accelerator.hardware)
			})
		}
	}
}
