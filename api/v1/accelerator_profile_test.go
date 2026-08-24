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
			Image:       "nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless",
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

func TestAcceleratorExporterProfileJSONRoundTripStructuredRuntime(t *testing.T) {
	const profileJSON = `{
		"accelerator_type":"ascend_npu",
		"metrics_exporter":{
			"name":"npu-exporter",
			"image":"example.com/ascend/npu-exporter:test",
			"backends":["kubernetes"],
			"command":["/usr/local/bin/npu-exporter"],
			"args":["-ip=0.0.0.0","-port=8082","-containerMode=containerd"],
			"port":8082,
			"readiness":{
				"http_path":"/metrics",
				"initial_delay_seconds":15,
				"period_seconds":5,
				"timeout_seconds":5,
				"failure_threshold":3
			},
			"runtime":{
				"privileged":true,
				"volumes":[
					{"name":"ascend-driver","host_path":{"path":"/usr/local/Ascend/driver","type":"directory"}},
					{"name":"containerd-socket","host_path":{"path":"/run/containerd/containerd.sock","type":"socket"}}
				],
				"volume_mounts":[
					{"name":"ascend-driver","mount_path":"/usr/local/Ascend/driver"},
					{"name":"containerd-socket","mount_path":"/run/containerd/containerd.sock","read_only":false}
				]
			}
		}
	}`

	profile := AcceleratorProfile{}
	require.NoError(t, json.Unmarshal([]byte(profileJSON), &profile))

	data, err := json.Marshal(profile)
	require.NoError(t, err)
	assert.JSONEq(t, profileJSON, string(data))
}
