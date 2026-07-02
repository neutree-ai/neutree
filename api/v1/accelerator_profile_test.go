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
			Runtime:     "nvidia",
			Env: map[string]string{
				"ACCELERATOR_TYPE": "gpu",
			},
			Options: []string{"--gpus all"},
		},
		EngineRuntime: &RuntimeConfig{
			ImageSuffix: "cuda-engine",
			Runtime:     "nvidia",
			Options:     []string{"--gpus", "all"},
		},
		MetricsExporter: &AcceleratorExporterProfile{
			Name:        "dcgm-exporter",
			Image:       "nvcr.io/nvidia/k8s/dcgm-exporter:3.3.9-3.6.1-ubuntu22.04",
			Args:        []string{"--collectors", "/etc/neutree/dcgm-exporter/default-counters.csv"},
			Port:        19400,
			MetricsPath: "/metrics",
			ConfigFiles: []AcceleratorExporterConfigFile{
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
		},
	}

	data, err := json.Marshal(profile)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"cluster_runtime"`)
	assert.Contains(t, string(data), `"engine_runtime"`)
	assert.Contains(t, string(data), `"metrics_exporter"`)
	assert.Contains(t, string(data), `"name":"dcgm-exporter"`)
	assert.NotContains(t, string(data), `"resource_defaults"`)
	assert.NotContains(t, string(data), `"raw_metrics"`)

	decoded := AcceleratorProfile{}
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, string(AcceleratorTypeNVIDIAGPU), decoded.AcceleratorType)
	require.NotNil(t, decoded.ClusterRuntime)
	assert.Equal(t, "cuda", decoded.ClusterRuntime.ImageSuffix)
	assert.Equal(t, "nvidia", decoded.ClusterRuntime.Runtime)
	assert.Equal(t, []string{"--gpus all"}, decoded.ClusterRuntime.Options)
	require.NotNil(t, decoded.EngineRuntime)
	assert.Equal(t, "cuda-engine", decoded.EngineRuntime.ImageSuffix)
	assert.Equal(t, "nvidia", decoded.EngineRuntime.Runtime)
	assert.Equal(t, []string{"--gpus", "all"}, decoded.EngineRuntime.Options)
	require.NotNil(t, decoded.MetricsExporter)
	assert.Equal(t, "dcgm-exporter", decoded.MetricsExporter.Name)
	assert.Equal(t, []string{"--collectors", "/etc/neutree/dcgm-exporter/default-counters.csv"}, decoded.MetricsExporter.Args)
	assert.Equal(t, 19400, decoded.MetricsExporter.Port)
	require.Len(t, decoded.MetricsExporter.ConfigFiles, 1)
	assert.Equal(t, "/etc/neutree/dcgm-exporter/default-counters.csv", decoded.MetricsExporter.ConfigFiles[0].Path)
	require.NotNil(t, decoded.MetricsExporter.Runtime)
	assert.True(t, decoded.MetricsExporter.Runtime.HostNetwork)
	require.NotNil(t, decoded.MetricsExporter.Runtime.Capabilities)
	assert.Equal(t, []string{"SYS_ADMIN"}, decoded.MetricsExporter.Runtime.Capabilities.Add)
	assert.Equal(t, map[string]string{"nvidia.com/gpu.present": "true"}, decoded.MetricsExporter.Runtime.NodeSelector)
	assert.Equal(t, []string{"--gpus all"}, decoded.MetricsExporter.Runtime.DockerRunOptions)
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
