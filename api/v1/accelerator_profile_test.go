package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
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
			Options: []string{"--volume /cluster-only:/cluster-only:ro"},
		},
		EngineRuntime: &RuntimeConfig{
			ImageSuffix: "cuda-engine",
			Runtime:     "nvidia",
			Options:     []string{"--gpus", "all"},
		},
		NodeAgentRuntime: &NodeAgentRuntimeProfile{
			Image:      "registry.example/neutree-node-agent:v1.2.0",
			Privileged: true,
			NodeSelector: map[string]string{
				"example.com/accelerator": "present",
			},
			Env: map[string]string{
				"NVIDIA_VISIBLE_DEVICES":     "all",
				"NVIDIA_DRIVER_CAPABILITIES": "utility,compute",
			},
			Capabilities: &corev1.Capabilities{Add: []corev1.Capability{corev1.Capability("SYS_ADMIN")}},
			Volumes: []ComponentVolume{{
				Name:     "vendor-driver",
				HostPath: &ComponentHostPathVolumeSource{Path: "/opt/vendor/driver", Type: ComponentHostPathTypeDirectory},
			}},
			VolumeMounts:     []ComponentVolumeMount{{Name: "vendor-driver", MountPath: "/opt/vendor/driver"}},
			Runtime:          "vendor-runtime",
			DockerRunOptions: []string{"--device=/dev/vendor0"},
		},
		VirtualizationMetricsTarget: &MetricsTargetProfile{
			Namespace: "kube-system",
			PodSelector: map[string]string{
				"app.kubernetes.io/component": "hami-device-plugin",
			},
			Port:        9394,
			MetricsPath: "/metrics",
		},
		ExternalMetricsTarget: &MetricsTargetProfile{
			Namespace:   "vendor-system",
			PodSelector: map[string]string{"app": "vendor-exporter"},
			Port:        9400,
			MetricsPath: "/metrics",
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
				HostNetwork:  true,
				Capabilities: &corev1.Capabilities{Add: []corev1.Capability{corev1.Capability("SYS_ADMIN")}},
				NodeSelector: map[string]string{
					"nvidia.com/gpu.present": "true",
				},
				Runtime:          "nvidia",
				DockerRunOptions: []string{"--gpus all"},
			},
		},
	}

	data, err := json.Marshal(profile)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"cluster_runtime"`)
	assert.Contains(t, string(data), `"engine_runtime"`)
	assert.Contains(t, string(data), `"node_agent_runtime"`)
	assert.Contains(t, string(data), `"virtualization_metrics_target"`)
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
	assert.Equal(t, []string{"--volume /cluster-only:/cluster-only:ro"}, decoded.ClusterRuntime.Options)
	require.NotNil(t, decoded.EngineRuntime)
	assert.Equal(t, "cuda-engine", decoded.EngineRuntime.ImageSuffix)
	assert.Equal(t, "nvidia", decoded.EngineRuntime.Runtime)
	assert.Equal(t, []string{"--gpus", "all"}, decoded.EngineRuntime.Options)
	require.NotNil(t, decoded.NodeAgentRuntime)
	assert.Equal(t, "registry.example/neutree-node-agent:v1.2.0", decoded.NodeAgentRuntime.Image)
	assert.True(t, decoded.NodeAgentRuntime.Privileged)
	assert.Equal(t, map[string]string{"example.com/accelerator": "present"}, decoded.NodeAgentRuntime.NodeSelector)
	assert.Equal(t, map[string]string{
		"NVIDIA_VISIBLE_DEVICES":     "all",
		"NVIDIA_DRIVER_CAPABILITIES": "utility,compute",
	}, decoded.NodeAgentRuntime.Env)
	require.NotNil(t, decoded.NodeAgentRuntime.Capabilities)
	assert.Equal(t, []corev1.Capability{corev1.Capability("SYS_ADMIN")}, decoded.NodeAgentRuntime.Capabilities.Add)
	assert.Equal(t, "vendor-runtime", decoded.NodeAgentRuntime.Runtime)
	assert.Equal(t, []string{"--device=/dev/vendor0"}, decoded.NodeAgentRuntime.DockerRunOptions)
	require.Len(t, decoded.NodeAgentRuntime.Volumes, 1)
	require.Len(t, decoded.NodeAgentRuntime.VolumeMounts, 1)
	require.NotNil(t, decoded.VirtualizationMetricsTarget)
	assert.Equal(t, "kube-system", decoded.VirtualizationMetricsTarget.Namespace)
	assert.Equal(t, map[string]string{"app.kubernetes.io/component": "hami-device-plugin"}, decoded.VirtualizationMetricsTarget.PodSelector)
	assert.Equal(t, 9394, decoded.VirtualizationMetricsTarget.Port)
	assert.Equal(t, "/metrics", decoded.VirtualizationMetricsTarget.MetricsPath)
	require.NotNil(t, decoded.ExternalMetricsTarget)
	assert.Equal(t, "vendor-system", decoded.ExternalMetricsTarget.Namespace)
	assert.Equal(t, map[string]string{"app": "vendor-exporter"}, decoded.ExternalMetricsTarget.PodSelector)
	assert.Equal(t, 9400, decoded.ExternalMetricsTarget.Port)
	require.NotNil(t, decoded.MetricsExporter)
	assert.Equal(t, "dcgm-exporter", decoded.MetricsExporter.Name)
	assert.Equal(t, []string{"--collectors", "/etc/neutree/dcgm-exporter/default-counters.csv"}, decoded.MetricsExporter.Args)
	assert.Equal(t, 19400, decoded.MetricsExporter.Port)
	require.Len(t, decoded.MetricsExporter.ConfigFiles, 1)
	assert.Equal(t, "/etc/neutree/dcgm-exporter/default-counters.csv", decoded.MetricsExporter.ConfigFiles[0].Path)
	require.NotNil(t, decoded.MetricsExporter.Runtime)
	assert.True(t, decoded.MetricsExporter.Runtime.HostNetwork)
	require.NotNil(t, decoded.MetricsExporter.Runtime.Capabilities)
	assert.Equal(t, []corev1.Capability{corev1.Capability("SYS_ADMIN")}, decoded.MetricsExporter.Runtime.Capabilities.Add)
	assert.Equal(t, map[string]string{"nvidia.com/gpu.present": "true"}, decoded.MetricsExporter.Runtime.NodeSelector)
	assert.Equal(t, "nvidia", decoded.MetricsExporter.Runtime.Runtime)
	assert.Equal(t, []string{"--gpus all"}, decoded.MetricsExporter.Runtime.DockerRunOptions)
}

func TestAcceleratorProfileIgnoresLegacyNodeAgentJSON(t *testing.T) {
	profile := AcceleratorProfile{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"node_agent":{"image":"registry.example/neutree-node-agent:v1.2.0"}
	}`), &profile))

	assert.Nil(t, profile.NodeAgentRuntime)
}

func TestAcceleratorProfileJSONRoundTripPreservesVirtualizationMetricsTarget(t *testing.T) {
	const profileJSON = `{
		"accelerator_type":"vendor_accelerator",
		"virtualization_metrics_target":{
			"namespace":"kube-system",
			"pod_selector":{"app.kubernetes.io/component":"vendor-device-plugin"},
			"port":9395,
			"metrics_path":"/metrics"
		}
	}`

	profile := AcceleratorProfile{}
	require.NoError(t, json.Unmarshal([]byte(profileJSON), &profile))

	data, err := json.Marshal(profile)
	require.NoError(t, err)
	assert.JSONEq(t, profileJSON, string(data))
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
		"accelerator_type":"vendor_accelerator",
		"metrics_exporter":{
			"name":"vendor-exporter",
			"image":"example.com/vendor/exporter:test",
			"command":["/usr/local/bin/vendor-exporter"],
			"args":["-ip=0.0.0.0","-port=8082","-containerMode=containerd"],
			"port":8082,
			"runtime":{
				"privileged":true,
				"volumes":[
					{"name":"vendor-driver","host_path":{"path":"/usr/local/vendor/driver","type":"directory"}},
					{"name":"containerd-socket","host_path":{"path":"/run/containerd/containerd.sock","type":"socket"}}
				],
				"volume_mounts":[
					{"name":"vendor-driver","mount_path":"/usr/local/vendor/driver"},
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
