package metrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/operator"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

func TestMetricsRendersProfileInitContainersAndRuntime(t *testing.T) {
	for _, test := range []struct {
		name, clientLabel string
		managed, operator bool
	}{
		{name: "legacy keeps shared agent"},
		{name: "operator with OS dependencies splits agents", operator: true},
		{name: "managed GPU ready", managed: true, operator: true, clientLabel: "true"},
		{name: "managed GPU recovering", managed: true, operator: true, clientLabel: "false"},
		{name: "managed client label not initialized", managed: true, operator: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := acceleratormocks.NewMockManager(t)
			manager.On("SupportPlugins").Return([]string{v1.AcceleratorTypeNVIDIAGPU.String()})
			manager.On("GetAcceleratorProfile", mock.Anything, metricsProfileInput(v1.AcceleratorTypeNVIDIAGPU.String())).Return(metricsNvidiaProfile(test.managed), nil)
			nodeLabels := map[string]string{accelerator.NvidiaGPUDiscoveryLabelKey: accelerator.NvidiaGPUDiscoveryLabelValue}
			if test.clientLabel != "" {
				nodeLabels[accelerator.NvidiaGPUClientLabel] = test.clientLabel
			}
			component := &MetricsComponent{
				cluster:     &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster", Workspace: "workspace"}, Spec: &v1.ClusterSpec{Version: "v1.2.0"}},
				imagePrefix: "registry.example.com/neutree",
				namespace:   "neutree-system", imagePullSecret: "pull-secret", metricsRemoteWriteURL: "https://metrics.example/write",
				acceleratorMgr: manager, ctrlClient: fake.NewClientBuilder().WithObjects(
					metricsTestNode("gpu", nodeLabels), metricsTestNode("cpu", nil),
				).Build(),
			}

			if test.operator {
				component.WithAcceleratorOperators(map[string]accelerator.AcceleratorOperator{accelerator.NvidiaAcceleratorType: operator.NewNvidiaAcceleratorOperator()})
			}
			resources, err := component.GetMetricsResources(context.Background())
			require.NoError(t, err)
			agent := findMetricsDaemonSet(t, resources, neutreeNodeAgentMetricsName)
			exporter := findMetricsDaemonSet(t, resources, "nvidia-gpu-dcgm-exporter")
			if !test.operator {
				require.False(t, hasMetricsDaemonSet(resources, cpuNodeAgentMetricsName))
				require.Empty(t, agent.Spec.Template.Spec.NodeSelector)
				require.Nil(t, agent.Spec.Template.Spec.Affinity)
				return
			}

			for _, daemonSet := range []*corev1.PodSpec{&agent.Spec.Template.Spec, &exporter.Spec.Template.Spec} {
				if !test.managed {
					require.Nil(t, daemonSet.RuntimeClassName)
					require.Empty(t, daemonSet.InitContainers)
					require.NotContains(t, daemonSet.NodeSelector, accelerator.NvidiaGPUClientLabel)
					continue
				}
				require.NotNil(t, daemonSet.RuntimeClassName)
				require.Equal(t, "nvidia", *daemonSet.RuntimeClassName)
				require.Len(t, daemonSet.InitContainers, 1)
				require.Equal(t, daemonSet.Containers[0].Image, daemonSet.InitContainers[0].Image)
				require.Equal(t, "true", daemonSet.NodeSelector[accelerator.NvidiaGPUClientLabel])
			}
			require.Equal(t, "all", envValue(agent.Spec.Template.Spec.Containers[0].Env, "NVIDIA_VISIBLE_DEVICES"))
			require.Equal(t, accelerator.NvidiaGPUDiscoveryLabelValue, agent.Spec.Template.Spec.NodeSelector[accelerator.NvidiaGPUDiscoveryLabelKey])

			cpuAgent := findMetricsDaemonSet(t, resources, cpuNodeAgentMetricsName)
			cpu := cpuAgent.Spec.Template.Spec
			require.Nil(t, cpu.RuntimeClassName)
			require.Empty(t, cpu.InitContainers)
			require.Empty(t, cpu.NodeSelector)
			require.False(t, hasEnv(cpu.Containers[0].Env, "NVIDIA_VISIBLE_DEVICES"))
			require.NotContains(t, cpu.Containers[0].Args, "--accelerator-type="+v1.AcceleratorTypeNVIDIAGPU.String())
			require.NotNil(t, cpu.Affinity)
			terms := cpu.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
			require.Len(t, terms, 1)
			require.Equal(t, []corev1.NodeSelectorRequirement{{
				Key: accelerator.NvidiaGPUDiscoveryLabelKey, Operator: corev1.NodeSelectorOpNotIn, Values: []string{accelerator.NvidiaGPUDiscoveryLabelValue},
			}}, terms[0].MatchExpressions)
			selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: terms[0].MatchExpressions[0].Key, Operator: metav1.LabelSelectorOpNotIn, Values: terms[0].MatchExpressions[0].Values,
			}}})
			require.NoError(t, err)
			require.True(t, selector.Matches(labels.Set{}), "CPU nodes remain covered")
			require.False(t, selector.Matches(labels.Set(nodeLabels)), "GPU client recovery cannot fall back to the CPU agent")
			findMetricsClusterRoleByApp(t, resources, cpuNodeAgentMetricsName)
			require.Equal(t, "node-agent", agent.Spec.Template.Labels["neutree.ai/metrics-target"])
			require.Equal(t, "node-agent", cpuAgent.Spec.Template.Labels["neutree.ai/metrics-target"])
			config := findMetricsConfigMap(t, resources, "vmagent-config").Data["prometheus.yml"]
			count := 0
			for _, job := range parsedScrapeJobs(t, config) {
				if job["job_name"] == neutreeNodeAgentMetricsName {
					count++
				}
			}
			require.Equal(t, 1, count, "one unchanged scrape job discovers both agents")
			require.True(t, configHasSelector(t, config, "neutree.ai/metrics-target=node-agent"))
		})
	}
}

func metricsNvidiaProfile(managed bool) *v1.AcceleratorProfile {
	profile := &v1.AcceleratorProfile{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		MetricsExporter: &v1.AcceleratorExporterProfile{
			Name: "dcgm-exporter", Image: "example/dcgm:v1", Port: 19400,
			Runtime: &v1.AcceleratorExporterRuntimeProfile{NodeSelector: map[string]string{
				accelerator.NvidiaGPUDiscoveryLabelKey: accelerator.NvidiaGPUDiscoveryLabelValue,
			}},
		},
		NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{Env: map[string]string{"NVIDIA_VISIBLE_DEVICES": "all"}},
	}
	if managed {
		profile.NodeAgentRuntime.Runtime = "nvidia"
		profile.NodeAgentRuntime.InitContainers = []corev1.Container{{Name: "toolkit-validation"}}
		profile.NodeAgentRuntime.NodeSelector = map[string]string{accelerator.NvidiaGPUClientLabel: "true"}
		profile.MetricsExporter.Runtime.Runtime = "nvidia"
		profile.MetricsExporter.Runtime.InitContainers = []corev1.Container{{Name: "toolkit-validation", Image: profile.MetricsExporter.Image}}
		profile.MetricsExporter.Runtime.NodeSelector[accelerator.NvidiaGPUClientLabel] = "true"
	}
	return profile
}
