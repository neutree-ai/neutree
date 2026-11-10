package manifests_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/prompbmarshal"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promrelabel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/operator"
	"github.com/neutree-ai/neutree/internal/accelerator/operator/manifests"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/pkg/metricsconfig"
)

func TestNvidiaRendererDeclaresSameTargetsAsNodeAgent(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed", true: "external"}[external], func(t *testing.T) {
			profile, err := (&plugin.GPUAcceleratorPlugin{}).GetAcceleratorProfile(context.Background())
			require.NoError(t, err)
			profile.MetricsExporter.Port = 19500
			profile.MetricsExporter.MetricsPath = "/managed-metrics"
			profile.ExternalMetricsTarget.Port = 9401
			profile.ExternalMetricsTarget.MetricsPath = "/external-metrics"
			profile.VirtualizationMetricsTarget.Namespace = "monitor-namespace"
			profile.VirtualizationMetricsTarget.Port = 9395
			profile.VirtualizationMetricsTarget.MetricsPath = "/monitor-metrics"
			mode := v1.ClusterAcceleratorExporterModeManaged
			if external {
				mode = v1.ClusterAcceleratorExporterModeExternal
			}
			opCtx := &operator.OperatorContext{
				Cluster: &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "workspace-a"}, Spec: &v1.ClusterSpec{
					Version: "v1.2.0", Config: &v1.ClusterConfig{Metrics: &v1.ClusterMetricsConfig{
						AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{Mode: mode},
					}},
				}},
				Client: fake.NewClientBuilder().Build(), Namespace: "neutree-system",
			}
			renderer := operator.NewNvidiaResourceRenderer(nodeAgentProfiles{profile: profile})
			resources, err := renderer.Render(context.Background(), opCtx, operator.ProviderState{}, operator.ComponentPlan{})
			require.NoError(t, err)
			opCtx.Cluster.Spec.AcceleratorVirtualization = &v1.AcceleratorVirtualizationSpec{Enabled: true}
			targets, err := renderer.MetricsTargets(context.Background(), opCtx)
			require.NoError(t, err)
			require.Len(t, targets, 3)
			byJob := map[string]metricsconfig.Target{}
			for _, target := range targets {
				byJob[target.JobName] = target
			}
			ds := renderedNodeAgent(t, resources)
			env := literalEnv(ds.Spec.Template.Spec.Containers[0].Env)
			var selector map[string]string
			require.NoError(t, json.Unmarshal([]byte(env[v1.AcceleratorExporterPodSelectorEnvKey]), &selector))
			exporter := byJob["accelerator-exporter-nvidia-gpu"]
			assert.Equal(t, metricsconfig.PodSelectors(selector), exporter.Selectors)
			assert.Equal(t, env[v1.AcceleratorExporterNamespaceEnvKey], exporter.Namespace)
			if external {
				assert.Equal(t, 9401, exporter.Port)
				assert.Equal(t, "/external-metrics", exporter.MetricsPath)
				assert.Empty(t, exporter.Namespace)
				assert.NotContains(t, selector, manifests.OwnerLabel)
			} else {
				assert.Equal(t, 19500, exporter.Port)
				assert.Equal(t, "/managed-metrics", exporter.MetricsPath)
				assert.Equal(t, manifests.OwnerValue, selector[manifests.OwnerLabel])
			}
			nodeAgent := byJob["nvidia-node-agent"]
			assert.Equal(t, "neutree-system", nodeAgent.Namespace)
			assert.Equal(t, 19101, nodeAgent.Port)
			assert.Equal(t, metricsconfig.PodSelectors(map[string]string{
				manifests.OwnerLabel: manifests.OwnerValue, manifests.DeploymentModeLabel: manifests.DeploymentModeValue,
				v1.NeutreeClusterLabelKey: "cluster-a", v1.NeutreeClusterWorkspaceLabelKey: "workspace-a", manifests.MetricsTargetLabel: "node-agent",
			}), nodeAgent.Selectors)
			monitor := byJob["hami-vgpu-monitor"]
			assert.Equal(t, "monitor-namespace", monitor.Namespace)
			assert.Equal(t, 9395, monitor.Port)
			assert.Equal(t, "/monitor-metrics", monitor.MetricsPath)
			assert.Equal(t, metricsconfig.PodSelectors(profile.VirtualizationMetricsTarget.PodSelector), monitor.Selectors)

			for _, test := range []struct {
				job       string
				labels    map[string]string
				forbidden []string
			}{
				{job: "nvidia-node-agent", labels: map[string]string{"node": "gpu-node"}, forbidden: []string{"accelerator_type"}},
				{job: "accelerator-exporter-nvidia-gpu", labels: map[string]string{"node": "gpu-node", "accelerator_type": "nvidia_gpu"}},
				{job: "hami-vgpu-monitor", labels: map[string]string{"node": "gpu-node", "monitor_namespace": "monitor-namespace", "monitor_pod": "producer-pod"}, forbidden: []string{"namespace", "pod"}},
			} {
				parsed, err := promrelabel.ParseRelabelConfigs(byJob[test.job].RelabelConfigs)
				require.NoError(t, err)
				result := parsed.Apply([]prompbmarshal.Label{
					{Name: "__meta_kubernetes_pod_ip", Value: "10.0.0.1"}, {Name: "__meta_kubernetes_pod_node_name", Value: "gpu-node"},
					{Name: "__meta_kubernetes_namespace", Value: "monitor-namespace"}, {Name: "__meta_kubernetes_pod_name", Value: "producer-pod"},
				}, 0)
				labels := map[string]string{}
				for _, label := range result {
					labels[label.Name] = label.Value
				}
				for name, value := range test.labels {
					require.Equal(t, value, labels[name], test.job)
				}
				for _, name := range test.forbidden {
					require.NotContains(t, labels, name, test.job)
				}
			}
		})
	}
}
