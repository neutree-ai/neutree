package metrics

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
	acceleratorMgr.On("GetAcceleratorProfile", mock.Anything, "vendor_accelerator").Return(&v1.AcceleratorProfile{
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
	assert.Contains(t, vmagentConfig, "label: app=vendor-exporter")
	assert.Contains(t, vmagentConfig, "names:\n      - vendor-system")
	assert.Contains(t, vmagentConfig, "replacement: $1:19401")
	assert.Contains(t, vmagentConfig, "metrics_path: /custom-metrics")
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

func TestBuildMetricsResourcesUsesExternalTargetWithoutManagedExporterProfile(t *testing.T) {
	acceleratorMgr := &acceleratormocks.MockManager{}
	acceleratorMgr.On("SupportPlugins").Return([]string{"vendor_accelerator"}).Maybe()
	acceleratorMgr.On("GetAcceleratorProfile", mock.Anything, "vendor_accelerator").Return(&v1.AcceleratorProfile{
		AcceleratorType: "vendor_accelerator",
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{
			Image: "example.com/neutree-node-agent:v1.2.0",
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
	}

	objects, err := component.GetMetricsResources(context.Background())
	require.NoError(t, err)
	assert.False(t, hasMetricsDaemonSet(objects, "vendor-accelerator-external"))

	vmagentConfig := findMetricsConfigMap(t, objects, "vmagent-config").Data["prometheus.yml"]
	assert.Contains(t, vmagentConfig, "label: app=upstream-exporter")
	assert.Contains(t, vmagentConfig, "names:\n      - upstream-system")
	assert.Contains(t, vmagentConfig, "replacement: $1:19402")
	assert.Contains(t, vmagentConfig, "metrics_path: /metrics/custom")

	nodeAgent := findMetricsDaemonSet(t, objects, neutreeNodeAgentMetricsName)
	args := nodeAgent.Spec.Template.Spec.Containers[0].Args
	assert.Contains(t, args, "--accelerator-exporter-port=19402")
	assert.Contains(t, args, "--accelerator-exporter-metrics-path=/metrics/custom")
}

func TestBuildVMAgentConfigUsesProfileVirtualizationMetricsTarget(t *testing.T) {
	acceleratorMgr := &acceleratormocks.MockManager{}
	acceleratorMgr.On("SupportPlugins").Return([]string{"vendor_accelerator"}).Maybe()
	acceleratorMgr.On("GetAcceleratorProfile", mock.Anything, "vendor_accelerator").Return(&v1.AcceleratorProfile{
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
	}

	objects, err := component.GetMetricsResources(context.Background())
	require.NoError(t, err)
	vmagentConfig := findMetricsConfigMap(t, objects, "vmagent-config").Data["prometheus.yml"]
	assert.Contains(t, vmagentConfig, "job_name: 'hami-vgpu-monitor'")
	assert.Contains(t, vmagentConfig, "names:\n      - vendor-system")
	assert.Contains(t, vmagentConfig, "label: app.kubernetes.io/component=vendor-device-plugin")
	assert.Contains(t, vmagentConfig, "replacement: $1:9395")
	assert.Contains(t, vmagentConfig, "metrics_path: /monitor-metrics")
	assert.NotContains(t, vmagentConfig, "replacement: $1:9394")
}
