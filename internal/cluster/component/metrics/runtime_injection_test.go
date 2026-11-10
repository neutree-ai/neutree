package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

func TestMetricsRendersProfileInitContainersAndRuntime(t *testing.T) {
	for _, test := range []struct {
		name, clientLabel                       string
		managed, operator, matchError, external bool
	}{
		{name: "legacy keeps shared agent"},
		{name: "operator with OS dependencies splits agents", operator: true},
		{name: "managed GPU ready", managed: true, operator: true, clientLabel: "true"},
		{name: "external exporter keeps both agents", managed: true, operator: true, external: true, clientLabel: "true"},
		{name: "managed GPU recovering", managed: true, operator: true, clientLabel: "false"},
		{name: "managed client label not initialized", managed: true, operator: true},
		{name: "hardware discovery error stops rendering", operator: true, matchError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := acceleratormocks.NewMockManager(t)
			manager.On("SupportPlugins").Return([]string{v1.AcceleratorTypeNVIDIAGPU.String()})
			profile := metricsNvidiaProfile(test.managed)
			if test.operator {
				manager.On("GetPlugin", accelerator.NvidiaAcceleratorType).Return(&plugin.GPUAcceleratorPlugin{}, true)
			} else {
				profile.NodeAgentRuntime.NodeSelector = nil
				manager.On("GetPlugin", accelerator.NvidiaAcceleratorType).Return(metricsLegacyPlugin{resource: accelerator.NvidiaAcceleratorType}, true)
			}
			manager.On("GetAcceleratorProfile", mock.Anything, metricsProfileInput(v1.AcceleratorTypeNVIDIAGPU.String())).Return(profile, nil)
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
				).WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if test.matchError {
							return errors.New("node discovery denied")
						}
						return c.List(ctx, list, opts...)
					},
				}).Build(),
			}

			if test.external {
				component.cluster.Spec.Config = &v1.ClusterConfig{Metrics: &v1.ClusterMetricsConfig{AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{Mode: v1.ClusterAcceleratorExporterModeExternal}}}
			}
			resources, err := component.GetMetricsResources(context.Background())
			if test.matchError {
				require.ErrorContains(t, err, "node discovery denied")
				require.Nil(t, resources)
				return
			}
			require.NoError(t, err)
			agent := findMetricsDaemonSet(t, resources, neutreeNodeAgentMetricsName)
			pods := []*corev1.PodSpec{&agent.Spec.Template.Spec}
			if test.external {
				require.False(t, hasMetricsDaemonSet(resources, "nvidia-gpu-dcgm-exporter"))
				require.Contains(t, agent.Spec.Template.Spec.Containers[0].Args, "--accelerator-exporter-port=9400")
			} else {
				exporter := findMetricsDaemonSet(t, resources, "nvidia-gpu-dcgm-exporter")
				pods = append(pods, &exporter.Spec.Template.Spec)
			}
			if !test.operator {
				require.False(t, hasMetricsDaemonSet(resources, cpuNodeAgentMetricsName))
				require.Empty(t, agent.Spec.Template.Spec.NodeSelector)
				require.Nil(t, agent.Spec.Template.Spec.Affinity)
				return
			}

			for _, daemonSet := range pods {
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
			require.Equal(t, "cpu", cpuAgent.Spec.Template.Labels["neutree.ai/node-agent-type"])
			require.Equal(t, "cpu", cpuAgent.Labels["neutree.ai/node-agent-type"])
			require.NotContains(t, cpuAgent.Spec.Selector.MatchLabels, "neutree.ai/node-agent-type")
			require.Equal(t, "accelerator", agent.Spec.Template.Labels["neutree.ai/node-agent-type"])
			require.Equal(t, "accelerator", agent.Labels["neutree.ai/node-agent-type"])
			require.NotContains(t, agent.Spec.Selector.MatchLabels, "neutree.ai/node-agent-type")
			require.NotContains(t, cpu.Containers[0].Args, "--accelerator-type=cpu")
			require.NotNil(t, cpu.Affinity)
			terms := cpu.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
			require.Len(t, terms, 1)
			expected := []corev1.NodeSelectorRequirement{{
				Key: accelerator.NvidiaGPUDiscoveryLabelKey, Operator: corev1.NodeSelectorOpNotIn, Values: []string{accelerator.NvidiaGPUDiscoveryLabelValue},
			}}
			if test.managed {
				expected = append(expected, corev1.NodeSelectorRequirement{Key: accelerator.NvidiaGPUClientLabel, Operator: corev1.NodeSelectorOpNotIn, Values: []string{"true"}})
			}
			require.ElementsMatch(t, expected, terms[0].MatchExpressions)
			selector := cpuAgentAffinitySelector(t, cpu.Affinity)
			if test.managed {
				require.False(t, selector.Matches(labels.Set{accelerator.NvidiaGPUClientLabel: "true"}), "client-only nodes also stay outside the CPU agent scope")
			}
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
		NodeAgentRuntime:      &v1.NodeAgentRuntimeProfile{Env: map[string]string{"NVIDIA_VISIBLE_DEVICES": "all"}, NodeSelector: map[string]string{accelerator.NvidiaGPUDiscoveryLabelKey: "true"}},
		ExternalMetricsTarget: &v1.MetricsTargetProfile{Namespace: "gpu-operator", PodSelector: map[string]string{"app": "nvidia-dcgm-exporter"}, Port: 9400, MetricsPath: "/metrics"},
	}
	if managed {
		profile.NodeAgentRuntime.Runtime = "nvidia"
		profile.NodeAgentRuntime.InitContainers = []corev1.Container{{Name: "toolkit-validation"}}
		profile.NodeAgentRuntime.NodeSelector[accelerator.NvidiaGPUClientLabel] = "true"
		profile.MetricsExporter.Runtime.Runtime = "nvidia"
		profile.MetricsExporter.Runtime.InitContainers = []corev1.Container{{Name: "toolkit-validation", Image: profile.MetricsExporter.Image}}
		profile.MetricsExporter.Runtime.NodeSelector[accelerator.NvidiaGPUClientLabel] = "true"
	}
	return profile
}

func cpuAgentAffinitySelector(t *testing.T, affinity *corev1.Affinity) labels.Selector {
	t.Helper()
	require.NotNil(t, affinity)
	terms := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	require.Len(t, terms, 1)
	var expressions []metav1.LabelSelectorRequirement
	for _, requirement := range terms[0].MatchExpressions {
		expressions = append(expressions, metav1.LabelSelectorRequirement{
			Key: requirement.Key, Operator: metav1.LabelSelectorOperator(requirement.Operator), Values: requirement.Values,
		})
	}
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchExpressions: expressions})
	require.NoError(t, err)
	return selector
}
