package metrics

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
)

func TestBuildMetricsResourcesUsesProfileExternalTarget(t *testing.T) {
	acceleratorMgr := &acceleratormocks.MockManager{}
	acceleratorMgr.On("SupportPlugins").Return([]string{"vendor_accelerator"}).Maybe()
	acceleratorMgr.On("GetAcceleratorProfile", mock.Anything, metricsProfileInput("vendor_accelerator")).Return(&v1.AcceleratorProfile{
		AcceleratorType: "vendor_accelerator",
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{
			Image: "example.com/neutree-node-agent:v1.2.0",
			Env:   map[string]string{"NODE_AGENT_MODE": "external"},
		},
		MetricsExporter: &v1.AcceleratorExporterProfile{
			Name:  "vendor-exporter",
			Image: "example.com/vendor-exporter:v1",
			Port:  19400,
			Runtime: &v1.AcceleratorExporterRuntimeProfile{
				NodeSelector: map[string]string{"example.com/accelerator": "true"},
			},
		},
		ExternalMetricsTarget: &v1.MetricsTargetProfile{
			Namespace:   "vendor-system",
			PodSelector: map[string]string{"app": "vendor-exporter"},
			Port:        19401,
			MetricsPath: "/custom-metrics",
		},
	}, nil).Maybe()

	component := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "test-workspace"},
			Spec: &v1.ClusterSpec{
				Version: "v1.1.2",
				Config: &v1.ClusterConfig{Metrics: &v1.ClusterMetricsConfig{
					AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{
						Mode: v1.ClusterAcceleratorExporterModeExternal,
					},
				}},
			},
		},
		namespace:             "metrics-system",
		imagePullSecret:       "pull-secret",
		metricsRemoteWriteURL: "https://metrics.example.com/api/v1/write",
		acceleratorMgr:        acceleratorMgr,
		ctrlClient: fake.NewClientBuilder().WithObjects(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:   "accelerator-node",
			Labels: map[string]string{"example.com/accelerator": "true"},
		}}).Build(),
	}

	objects, err := component.GetMetricsResources(context.Background())
	require.NoError(t, err)
	assert.False(t, hasMetricsDaemonSet(objects, "vendor-accelerator-vendor-exporter"))

	vmagentConfig := findMetricsConfigMap(t, objects, "vmagent-config").Data["prometheus.yml"]
	assert.True(t, configHasSelector(t, vmagentConfig, "app=vendor-exporter"))
	assert.True(t, configHasNamespace(t, vmagentConfig, "vendor-system"))
	assert.Contains(t, vmagentConfig, "replacement: $1:19401")
	assert.True(t, configHasMetricsPath(t, vmagentConfig, "/custom-metrics"))
	assert.NotContains(t, vmagentConfig, "neutree.ai/metrics-target=accelerator-exporter")

	nodeAgent := findMetricsDaemonSet(t, objects, neutreeNodeAgentMetricsName)
	container := nodeAgent.Spec.Template.Spec.Containers[0]
	assert.Contains(t, container.Args, "--accelerator-exporter-port=19401")
	assert.Contains(t, container.Args, "--accelerator-exporter-metrics-path=/custom-metrics")
	assert.NotContains(t, strings.Join(container.Args, "\n"), "--metrics-mode=")
	assert.Equal(t, "external", envValue(container.Env, "NODE_AGENT_MODE"))
	assert.Equal(t, "vendor-system", envValue(container.Env, v1.AcceleratorExporterNamespaceEnvKey))

	selector := map[string]string{}
	require.NoError(t, json.Unmarshal([]byte(envValue(container.Env, v1.AcceleratorExporterPodSelectorEnvKey)), &selector))
	assert.Equal(t, map[string]string{"app": "vendor-exporter"}, selector)
}

func TestBuildMetricsResourcesExternalSelectsOnlyNodeMatchingProfile(t *testing.T) {
	acceleratorMgr := &acceleratormocks.MockManager{}
	acceleratorMgr.On("SupportPlugins").Return([]string{"other_accelerator", v1.AcceleratorTypeNVIDIAGPU.String()})
	acceleratorMgr.On("GetAcceleratorProfile", mock.Anything, metricsProfileInput(v1.AcceleratorTypeNVIDIAGPU.String())).Return(&v1.AcceleratorProfile{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{
			Image: "example.com/nvidia-node-agent:v1",
		},
		MetricsExporter: &v1.AcceleratorExporterProfile{
			Name:  "dcgm-exporter",
			Image: "example.com/dcgm-exporter:v1",
			Port:  19400,
			Runtime: &v1.AcceleratorExporterRuntimeProfile{
				NodeSelector: map[string]string{"nvidia.com/gpu.present": "true"},
			},
		},
		ExternalMetricsTarget: &v1.MetricsTargetProfile{
			PodSelector: map[string]string{"app": "nvidia-dcgm-exporter"},
			Port:        9400,
		},
	}, nil)
	acceleratorMgr.On("GetAcceleratorProfile", mock.Anything, metricsProfileInput("other_accelerator")).Return(&v1.AcceleratorProfile{
		AcceleratorType: "other_accelerator",
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{
			Image: "example.com/other-node-agent:v1",
		},
		MetricsExporter: &v1.AcceleratorExporterProfile{
			Name:  "other-exporter",
			Image: "example.com/other-exporter:v1",
			Port:  8082,
			Runtime: &v1.AcceleratorExporterRuntimeProfile{
				NodeSelector: map[string]string{"example.com/other-accelerator": "true"},
			},
		},
		ExternalMetricsTarget: &v1.MetricsTargetProfile{
			PodSelector: map[string]string{"app": "other-exporter"},
			Port:        8082,
		},
	}, nil)
	t.Cleanup(func() { acceleratorMgr.AssertExpectations(t) })

	component := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "test-workspace"},
			Spec: &v1.ClusterSpec{
				Version: "v1.1.2",
				Config: &v1.ClusterConfig{Metrics: &v1.ClusterMetricsConfig{
					AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{
						Mode: v1.ClusterAcceleratorExporterModeExternal,
					},
				}},
			},
		},
		namespace:             "metrics-system",
		metricsRemoteWriteURL: "https://metrics.example.com/api/v1/write",
		acceleratorMgr:        acceleratorMgr,
		ctrlClient: fake.NewClientBuilder().WithObjects(metricsTestNode("gpu-node", map[string]string{
			"nvidia.com/gpu.present": "true",
		})).Build(),
	}

	objects, err := component.GetMetricsResources(context.Background())
	require.NoError(t, err)
	assert.False(t, hasMetricsDaemonSet(objects, "nvidia-gpu-dcgm-exporter"))
	assert.False(t, hasMetricsDaemonSet(objects, "other-accelerator-other-exporter"))

	vmagentConfig := findMetricsConfigMap(t, objects, "vmagent-config").Data["prometheus.yml"]
	assert.True(t, configHasJob(t, vmagentConfig, "accelerator-exporter-nvidia-gpu"))
	assert.True(t, configHasSelector(t, vmagentConfig, "app=nvidia-dcgm-exporter"))
	assert.False(t, configHasJob(t, vmagentConfig, "accelerator-exporter-other-accelerator"))

	nodeAgent := findMetricsDaemonSet(t, objects, neutreeNodeAgentMetricsName)
	args := nodeAgent.Spec.Template.Spec.Containers[0].Args
	assert.Contains(t, args, "--accelerator-type=nvidia_gpu")
	assert.Contains(t, args, "--accelerator-exporter-port=9400")
}

func TestBuildMetricsResourcesUsesExternalTargetWithRuntimeSelector(t *testing.T) {
	acceleratorMgr := &acceleratormocks.MockManager{}
	acceleratorMgr.On("SupportPlugins").Return([]string{"vendor_accelerator"}).Maybe()
	acceleratorMgr.On("GetAcceleratorProfile", mock.Anything, metricsProfileInput("vendor_accelerator")).Return(&v1.AcceleratorProfile{
		AcceleratorType: "vendor_accelerator",
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{
			Image: "example.com/neutree-node-agent:v1.2.0",
		},
		MetricsExporter: &v1.AcceleratorExporterProfile{
			Name:  "upstream-exporter",
			Image: "example.com/upstream-exporter:v1",
			Port:  19400,
			Runtime: &v1.AcceleratorExporterRuntimeProfile{
				NodeSelector: map[string]string{"example.com/accelerator": "true"},
			},
		},
		ExternalMetricsTarget: &v1.MetricsTargetProfile{
			Namespace:   "upstream-system",
			PodSelector: map[string]string{"app": "upstream-exporter"},
			Port:        19402,
			MetricsPath: "/metrics/custom",
		},
	}, nil).Maybe()

	component := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "test-workspace"},
			Spec: &v1.ClusterSpec{
				Version: "v1.1.2",
				Config: &v1.ClusterConfig{Metrics: &v1.ClusterMetricsConfig{
					AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{
						Mode: v1.ClusterAcceleratorExporterModeExternal,
					},
				}},
			},
		},
		namespace:             "metrics-system",
		metricsRemoteWriteURL: "https://metrics.example.com/api/v1/write",
		acceleratorMgr:        acceleratorMgr,
		ctrlClient: fake.NewClientBuilder().WithObjects(metricsTestNode("accelerator-node", map[string]string{
			"example.com/accelerator": "true",
		})).Build(),
	}

	objects, err := component.GetMetricsResources(context.Background())
	require.NoError(t, err)
	assert.False(t, hasMetricsDaemonSet(objects, "vendor-accelerator-external"))

	vmagentConfig := findMetricsConfigMap(t, objects, "vmagent-config").Data["prometheus.yml"]
	assert.True(t, configHasSelector(t, vmagentConfig, "app=upstream-exporter"))
	assert.True(t, configHasNamespace(t, vmagentConfig, "upstream-system"))
	assert.Contains(t, vmagentConfig, "replacement: $1:19402")
	assert.True(t, configHasMetricsPath(t, vmagentConfig, "/metrics/custom"))

	nodeAgent := findMetricsDaemonSet(t, objects, neutreeNodeAgentMetricsName)
	args := nodeAgent.Spec.Template.Spec.Containers[0].Args
	assert.Contains(t, args, "--accelerator-exporter-port=19402")
	assert.Contains(t, args, "--accelerator-exporter-metrics-path=/metrics/custom")
}

func TestBuildMetricsResourcesUsesExternalTargetAcrossNamespacesWhenNamespaceEmpty(t *testing.T) {
	acceleratorMgr := &acceleratormocks.MockManager{}
	acceleratorMgr.On("SupportPlugins").Return([]string{"vendor_accelerator"}).Maybe()
	acceleratorMgr.On("GetAcceleratorProfile", mock.Anything, metricsProfileInput("vendor_accelerator")).Return(&v1.AcceleratorProfile{
		AcceleratorType: "vendor_accelerator",
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{
			Image: "example.com/neutree-node-agent:v1.2.0",
		},
		MetricsExporter: &v1.AcceleratorExporterProfile{
			Name:  "operator-exporter",
			Image: "example.com/operator-exporter:v1",
			Port:  19400,
			Runtime: &v1.AcceleratorExporterRuntimeProfile{
				NodeSelector: map[string]string{"example.com/accelerator": "true"},
			},
		},
		ExternalMetricsTarget: &v1.MetricsTargetProfile{
			PodSelector: map[string]string{"app": "operator-exporter"},
			Port:        19401,
			MetricsPath: "/operator-metrics",
		},
	}, nil).Maybe()

	component := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "test-workspace"},
			Spec: &v1.ClusterSpec{
				Version: "v1.1.2",
				Config: &v1.ClusterConfig{Metrics: &v1.ClusterMetricsConfig{
					AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{
						Mode: v1.ClusterAcceleratorExporterModeExternal,
					},
				}},
			},
		},
		namespace:             "neutree-system",
		metricsRemoteWriteURL: "https://metrics.example.com/api/v1/write",
		acceleratorMgr:        acceleratorMgr,
		ctrlClient: fake.NewClientBuilder().WithObjects(metricsTestNode("accelerator-node", map[string]string{
			"example.com/accelerator": "true",
		})).Build(),
	}

	objects, err := component.GetMetricsResources(context.Background())
	require.NoError(t, err)

	vmagentConfig := findMetricsConfigMap(t, objects, "vmagent-config").Data["prometheus.yml"]
	jobConfig := metricsScrapeJobConfig(t, vmagentConfig, acceleratorExporterJobName("vendor_accelerator"))
	assert.Contains(t, jobConfig, "label: app=operator-exporter")
	assert.Contains(t, jobConfig, "replacement: $1:19401")
	assert.NotContains(t, jobConfig, "namespaces:")
	assert.NotContains(t, jobConfig, "neutree-system")

	nodeAgent := findMetricsDaemonSet(t, objects, neutreeNodeAgentMetricsName)
	container := nodeAgent.Spec.Template.Spec.Containers[0]
	assert.Contains(t, container.Args, "--accelerator-exporter-port=19401")
	assert.Contains(t, container.Args, "--accelerator-exporter-metrics-path=/operator-metrics")
	assert.Empty(t, envValue(container.Env, v1.AcceleratorExporterNamespaceEnvKey))
	assert.False(t, containsEnv(container.Env, v1.AcceleratorExporterNamespaceEnvKey))
	assert.NotEmpty(t, envValue(container.Env, v1.AcceleratorExporterPodSelectorEnvKey))
}

func metricsScrapeJobConfig(t *testing.T, config string, jobName string) string {
	t.Helper()

	for _, job := range parsedScrapeJobs(t, config) {
		if job["job_name"] == jobName {
			data, err := yaml.Marshal(job)
			require.NoError(t, err)
			return string(data)
		}
	}
	t.Fatalf("job %q was not rendered", jobName)
	return ""
}

func containsEnv(env []corev1.EnvVar, name string) bool {
	for _, item := range env {
		if item.Name == name {
			return true
		}
	}

	return false
}

func TestBuildVMAgentConfigUsesProfileVirtualizationMetricsTarget(t *testing.T) {
	acceleratorMgr := &acceleratormocks.MockManager{}
	acceleratorMgr.On("SupportPlugins").Return([]string{"vendor_accelerator"}).Maybe()
	acceleratorMgr.On("GetAcceleratorProfile", mock.Anything, metricsProfileInput("vendor_accelerator")).Return(&v1.AcceleratorProfile{
		AcceleratorType: "vendor_accelerator",
		MetricsExporter: &v1.AcceleratorExporterProfile{
			Name:  "vendor-exporter",
			Image: "example.com/vendor-exporter:v1",
			Port:  19400,
		},
		VirtualizationMetricsTarget: &v1.MetricsTargetProfile{
			Namespace: "vendor-system",
			PodSelector: map[string]string{
				"app.kubernetes.io/component": "vendor-device-plugin",
			},
			Port:        9395,
			MetricsPath: "/monitor-metrics",
		},
	}, nil).Maybe()

	component := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "test-workspace"},
			Spec: &v1.ClusterSpec{
				Version:                   "v1.1.2",
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: true},
			},
		},
		namespace:             "metrics-system",
		metricsRemoteWriteURL: "https://metrics.example.com/api/v1/write",
		acceleratorMgr:        acceleratorMgr,
		ctrlClient:            fake.NewClientBuilder().WithObjects(metricsTestNode("cpu-node", nil)).Build(),
	}

	objects, err := component.GetMetricsResources(context.Background())
	require.NoError(t, err)
	vmagentConfig := findMetricsConfigMap(t, objects, "vmagent-config").Data["prometheus.yml"]
	assert.True(t, configHasJob(t, vmagentConfig, "hami-vgpu-monitor"))
	assert.True(t, configHasNamespace(t, vmagentConfig, "vendor-system"))
	assert.True(t, configHasSelector(t, vmagentConfig, "app.kubernetes.io/component=vendor-device-plugin"))
	assert.Contains(t, vmagentConfig, "replacement: $1:9395")
	assert.True(t, configHasMetricsPath(t, vmagentConfig, "/monitor-metrics"))
	assert.NotContains(t, vmagentConfig, "replacement: $1:9394")
}
