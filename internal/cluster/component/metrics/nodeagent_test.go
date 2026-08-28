package metrics

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
)

func TestMetricsResourcesProjectsSelectedRuntimeToSingleNodeAgent(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "accelerator-node",
			Labels: map[string]string{"example.com/accelerator": "present"},
		},
	}).Build()
	const profileJSON = `{
		"accelerator_type":"vendor_accelerator",
		"virtualization_metrics_target":{
			"namespace":"kube-system",
			"pod_selector":{"app.kubernetes.io/component":"vendor-device-plugin"},
			"port":9395,
			"metrics_path":"/metrics"
		},
		"node_agent_runtime":{
			"image":"registry.example/neutree-node-agent:v1.2.0",
			"privileged":true,
			"node_selector":{"example.com/accelerator":"present"},
			"runtime":"nvidia",
			"docker_run_options":["--gpus all"],
			"env":{"VENDOR_VISIBLE_DEVICES":"all","NODE_AGENT_MODE":"accelerator","NEUTREE_VIRTUALIZATION_METRICS_TARGET":"stale"},
			"capabilities":{"add":["SYS_ADMIN"]},
			"volumes":[{"name":"vendor-driver","host_path":{"path":"/opt/vendor/driver","type":"directory"}}],
			"volume_mounts":[{"name":"vendor-driver","mount_path":"/opt/vendor/driver"}]
		},
		"metrics_exporter":{
			"name":"vendor-exporter",
			"image":"registry.example/vendor-exporter:v1.0.0",
			"port":8082,
			"metrics_path":"vendor-metrics",
			"env":{"EXPORTER_ONLY":"not-for-node-agent"},
			"runtime":{"node_selector":{"example.com/accelerator":"present"}}
		}
	}`
	profile := &v1.AcceleratorProfile{}
	require.NoError(t, json.Unmarshal([]byte(profileJSON), profile))
	acceleratorMgr := acceleratormocks.NewMockManager(t)
	acceleratorMgr.EXPECT().SupportPlugins().Return([]string{"vendor_accelerator"}).Maybe()
	acceleratorMgr.EXPECT().GetAcceleratorProfile(mock.Anything, "vendor_accelerator").Return(profile, nil).Maybe()

	component := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "test-workspace"},
			Spec:     &v1.ClusterSpec{Version: "v1.1.2"},
		},
		namespace:       "test-namespace",
		imagePullSecret: "pull-secret",
		ctrlClient:      client,
		acceleratorMgr:  acceleratorMgr,
	}

	objects, err := component.GetMetricsResources(context.Background())
	require.NoError(t, err)

	nodeAgent := findMetricsDaemonSet(t, objects, neutreeNodeAgentMetricsName)
	container := nodeAgent.Spec.Template.Spec.Containers[0]

	assert.Contains(t, container.Args, "--accelerator-type=vendor_accelerator")
	assert.Contains(t, container.Args, "--accelerator-exporter-port=8082")
	assert.Contains(t, container.Args, "--accelerator-exporter-metrics-path=/vendor-metrics")
	assert.Nil(t, container.ReadinessProbe)
	assert.Equal(t, "accelerator", envValue(container.Env, "NODE_AGENT_MODE"))
	assert.False(t, hasEnv(container.Env, "EXPORTER_ONLY"))
	assert.Nil(t, nodeAgent.Spec.Template.Spec.Affinity)
	assert.Equal(t, map[string]string{"example.com/accelerator": "present"}, nodeAgent.Spec.Template.Spec.NodeSelector)
	assert.Equal(t, map[string]string{
		"app":       neutreeNodeAgentMetricsName,
		"cluster":   "test-cluster",
		"workspace": "test-workspace",
	}, nodeAgent.Spec.Selector.MatchLabels)
	require.NotNil(t, container.SecurityContext)
	assert.True(t, *container.SecurityContext.Privileged)
	assert.Contains(t, container.SecurityContext.Capabilities.Add, corev1.Capability("SYS_ADMIN"))
	assert.NotContains(t, container.Args, "--runtime=nvidia")
	assert.NotContains(t, container.Args, "--gpus all")
	assert.Contains(t, daemonSetHostPathNames(nodeAgent), "vendor-driver")
	assert.Equal(t, "all", envValue(container.Env, "VENDOR_VISIBLE_DEVICES"))
	assert.JSONEq(t, `{"namespace":"kube-system","pod_selector":{"app.kubernetes.io/component":"vendor-device-plugin"},"port":9395,"metrics_path":"/metrics"}`,
		envValue(container.Env, v1.VirtualizationMetricsTargetEnvKey))
	assert.False(t, hasMetricsDaemonSet(objects, neutreeNodeAgentMetricsName+"-vendor-accelerator"))
}

func TestSelectedMetricsNodeAgentUsesLegacyContractBeforeV112(t *testing.T) {
	nodeAgent, err := selectedMetricsNodeAgent("v1.1.1", []metricsAcceleratorPlan{{
		AcceleratorType: "vendor_accelerator",
	}}, []metricsScrapeTarget{{
		AcceleratorType: "vendor_accelerator",
		Port:            8082,
		MetricsPath:     "vendor-metrics",
	}})

	require.NoError(t, err)
	assert.True(t, nodeAgent.UseLegacyContract)
	assert.Empty(t, nodeAgent.AcceleratorType)
	assert.Zero(t, nodeAgent.AcceleratorExporterPort)
	assert.Empty(t, nodeAgent.Env)
}

func TestSelectedMetricsNodeAgentUsesLegacyContractWithoutTargets(t *testing.T) {
	nodeAgent, err := selectedMetricsNodeAgent("v1.1.1", nil, nil)

	require.NoError(t, err)
	assert.True(t, nodeAgent.UseLegacyContract)
	assert.Empty(t, nodeAgent.AcceleratorType)
	assert.Zero(t, nodeAgent.AcceleratorExporterPort)
	assert.Empty(t, nodeAgent.Env)
}

func TestSelectedMetricsNodeAgentProjectsProfileTargetWithoutRuntime(t *testing.T) {
	nodeAgent, err := selectedMetricsNodeAgent("v1.1.2", []metricsAcceleratorPlan{{
		AcceleratorType: "vendor_accelerator",
		VirtualizationMetricsTarget: &v1.MetricsTargetProfile{
			Namespace: "kube-system",
			PodSelector: map[string]string{
				"app.kubernetes.io/component": "vendor-device-plugin",
			},
			Port:        9395,
			MetricsPath: "/metrics",
		},
	}}, []metricsScrapeTarget{{
		AcceleratorType: "vendor_accelerator",
		Port:            8082,
		MetricsPath:     "vendor-metrics",
	}})

	require.NoError(t, err)
	assert.Equal(t, "vendor_accelerator", nodeAgent.AcceleratorType)
	assert.Equal(t, 8082, nodeAgent.AcceleratorExporterPort)
	assert.Equal(t, "vendor-metrics", nodeAgent.AcceleratorExporterMetricsPath)
	assert.JSONEq(t, `{"namespace":"kube-system","pod_selector":{"app.kubernetes.io/component":"vendor-device-plugin"},"port":9395,"metrics_path":"/metrics"}`,
		envValue(nodeAgent.Env, v1.VirtualizationMetricsTargetEnvKey))
}

func TestSelectedMetricsNodeAgentProjectsTargetWithoutRuntimeProfile(t *testing.T) {
	nodeAgent, err := selectedMetricsNodeAgent("v1.1.2", []metricsAcceleratorPlan{{
		AcceleratorType: "vendor_accelerator",
	}}, []metricsScrapeTarget{{
		AcceleratorType: "vendor_accelerator",
		Port:            8082,
		MetricsPath:     "vendor-metrics",
	}})

	require.NoError(t, err)
	assert.Equal(t, "vendor_accelerator", nodeAgent.AcceleratorType)
	assert.False(t, nodeAgent.UseLegacyContract)
	assert.Equal(t, 8082, nodeAgent.AcceleratorExporterPort)
	assert.Equal(t, "vendor-metrics", nodeAgent.AcceleratorExporterMetricsPath)
	assert.Empty(t, nodeAgent.Env)
}

func daemonSetHostPathNames(daemonSet *appsv1.DaemonSet) []string {
	names := make([]string, 0, len(daemonSet.Spec.Template.Spec.Volumes))
	for _, volume := range daemonSet.Spec.Template.Spec.Volumes {
		if volume.HostPath != nil {
			names = append(names, volume.Name)
		}
	}

	return names
}

func hasMetricsDaemonSet(objects *unstructured.UnstructuredList, name string) bool {
	for _, object := range objects.Items {
		if object.GetKind() == "DaemonSet" && object.GetName() == name {
			return true
		}
	}

	return false
}
