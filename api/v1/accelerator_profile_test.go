package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcceleratorProfileJSONRoundTrip(t *testing.T) {
	profile := AcceleratorProfile{
		AcceleratorType: string(AcceleratorTypeNVIDIAGPU),
		ClusterRuntime: &RuntimeConfig{
			ImageSuffix: "cuda",
		},
		EndpointRuntime: &RuntimeConfig{
			Runtime: "nvidia",
			Options: []string{"--gpus all"},
			Env: map[string]string{
				"ACCELERATOR_TYPE": "gpu",
			},
		},
		Metrics: &AcceleratorMetricsProfile{
			Exporter: &AcceleratorExporterProfile{
				Kind:          "dcgm-exporter",
				ComponentType: NodeComponentTypeAcceleratorExporter,
				Image:         "nvcr.io/nvidia/k8s/dcgm-exporter:3.3.9-3.6.1-ubuntu22.04",
				Args:          []string{"--collectors", "/etc/neutree/dcgm-exporter/default-counters.csv"},
				Port:          9400,
				MetricsPath:   "/metrics",
				ConfigFiles: []NodeComponentConfigFile{
					{
						Path:    "/etc/neutree/dcgm-exporter/default-counters.csv",
						Content: "DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature.",
					},
				},
				Runtime: &AcceleratorExporterRuntimeProfile{
					HostNetwork: true,
					Capabilities: &AcceleratorExporterCapabilities{
						Add: []string{"SYS_ADMIN"},
					},
					NodeSelector: map[string]string{
						"nvidia.com/gpu.present": "true",
					},
					DockerRunOptions: []string{"--gpus all"},
				},
				RawMetrics: true,
			},
		},
		ResourceDefaults: &AcceleratorResourceDefaults{
			RayResourceName:        "GPU",
			KubernetesResourceName: "nvidia.com/gpu",
		},
	}

	data, err := json.Marshal(profile)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"component_type":"accelerator-exporter"`)
	assert.Contains(t, string(data), `"ray_resource_name":"GPU"`)

	decoded := AcceleratorProfile{}
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, string(AcceleratorTypeNVIDIAGPU), decoded.AcceleratorType)
	require.NotNil(t, decoded.ClusterRuntime)
	assert.Equal(t, "cuda", decoded.ClusterRuntime.ImageSuffix)
	require.NotNil(t, decoded.EndpointRuntime)
	assert.Equal(t, "nvidia", decoded.EndpointRuntime.Runtime)
	require.NotNil(t, decoded.Metrics)
	require.NotNil(t, decoded.Metrics.Exporter)
	assert.Equal(t, NodeComponentTypeAcceleratorExporter, decoded.Metrics.Exporter.ComponentType)
	assert.Equal(t, []string{"--collectors", "/etc/neutree/dcgm-exporter/default-counters.csv"}, decoded.Metrics.Exporter.Args)
	assert.Equal(t, 9400, decoded.Metrics.Exporter.Port)
	require.Len(t, decoded.Metrics.Exporter.ConfigFiles, 1)
	assert.Equal(t, "/etc/neutree/dcgm-exporter/default-counters.csv", decoded.Metrics.Exporter.ConfigFiles[0].Path)
	require.NotNil(t, decoded.Metrics.Exporter.Runtime)
	assert.True(t, decoded.Metrics.Exporter.Runtime.HostNetwork)
	require.NotNil(t, decoded.Metrics.Exporter.Runtime.Capabilities)
	assert.Equal(t, []string{"SYS_ADMIN"}, decoded.Metrics.Exporter.Runtime.Capabilities.Add)
	assert.Equal(t, map[string]string{"nvidia.com/gpu.present": "true"}, decoded.Metrics.Exporter.Runtime.NodeSelector)
	assert.Equal(t, []string{"--gpus all"}, decoded.Metrics.Exporter.Runtime.DockerRunOptions)
	require.NotNil(t, decoded.ResourceDefaults)
	assert.Equal(t, "nvidia.com/gpu", decoded.ResourceDefaults.KubernetesResourceName)
}

func TestGetAcceleratorProfileResponse(t *testing.T) {
	response := GetAcceleratorProfileResponse{
		Profile: AcceleratorProfile{
			AcceleratorType: string(AcceleratorTypeAMDGPU),
		},
	}

	data, err := json.Marshal(response)
	require.NoError(t, err)
	assert.JSONEq(t, `{"profile":{"accelerator_type":"amd_gpu"}}`, string(data))
}
