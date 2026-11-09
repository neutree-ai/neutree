package metrics

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestScrapeConfigPreservesLegacyRendering(t *testing.T) {
	data, err := os.ReadFile("testdata/legacy_scrape_jobs.yaml")
	require.NoError(t, err)
	var baseline struct {
		Global       map[string]interface{}            `yaml:"global"`
		Order        []string                          `yaml:"order"`
		MonitorOrder []string                          `yaml:"monitor_order"`
		Jobs         map[string]map[string]interface{} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &baseline))
	for _, test := range []struct {
		name      string
		namespace string
		selector  map[string]string
		port      int
		path      string
		monitor   bool
		expected  string
	}{
		{name: "managed defaults", namespace: "neutree-system", selector: managedAcceleratorExporterSelector("nvidia_gpu"), port: 19400, path: "/metrics", expected: "managed_exporter"},
		{name: "virtualization monitor", namespace: "neutree-system", selector: managedAcceleratorExporterSelector("nvidia_gpu"), port: 19400, path: "/metrics", monitor: true, expected: "managed_exporter"},
		{name: "external custom namespace and path", namespace: "operator-system", selector: map[string]string{"app": "nvidia-dcgm-exporter"}, port: 9400, path: "/custom-metrics", expected: "external_custom"},
		{name: "external all namespaces", selector: map[string]string{"app": "nvidia-dcgm-exporter"}, port: 9400, path: "/metrics", expected: "external_global"},
	} {
		t.Run(test.name, func(t *testing.T) {
			component := &MetricsComponent{namespace: "neutree-system", cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "workspace-a"}, Spec: &v1.ClusterSpec{Version: "v1.2.1"},
			}}
			plan := metricsAcceleratorPlan{AcceleratorType: "nvidia_gpu", ExporterTarget: &v1.MetricsTargetProfile{
				Namespace: test.namespace, PodSelector: test.selector, Port: test.port, MetricsPath: test.path,
			}}
			if test.monitor {
				component.cluster.Spec.AcceleratorVirtualization = &v1.AcceleratorVirtualizationSpec{Enabled: true}
				plan.VirtualizationMetricsTarget = &v1.MetricsTargetProfile{Namespace: "neutree-system",
					PodSelector: map[string]string{"app.kubernetes.io/component": "hami-device-plugin"}, Port: 9394, MetricsPath: "/metrics"}
			}
			vars := component.buildManifestVariables()
			vars.EnableNodeExporter, vars.EnableNeutreeNodeAgentMetrics, vars.EnableKubeStateMetrics = true, true, true
			vars.ScrapeTargets = append(vars.ScrapeTargets, component.nodeAgentScrapeTargets(neutreeNodeAgentMetricsName)...)
			vars.ScrapeTargets = append(vars.ScrapeTargets, component.acceleratorExporterScrapeTargets([]metricsAcceleratorPlan{plan})...)
			vars.ScrapeTargets = append(vars.ScrapeTargets, component.virtualizationMetricsScrapeTargets([]metricsAcceleratorPlan{plan})...)
			rendered, err := renderKubernetesVMAgentConfig(vars)
			require.NoError(t, err)
			var config struct {
				Global map[string]interface{}   `yaml:"global"`
				Jobs   []map[string]interface{} `yaml:"scrape_configs"`
			}
			require.NoError(t, yaml.Unmarshal([]byte(rendered), &config))
			require.Equal(t, baseline.Global, config.Global)
			expected := map[string]map[string]interface{}{
				"neutree-router":             baseline.Jobs["neutree-router"],
				"neutree-kube-state-metrics": baseline.Jobs["neutree-kube-state-metrics"],
				"neutree-node-agent":         baseline.Jobs["node_agent"], "accelerator-exporter-nvidia-gpu": baseline.Jobs[test.expected],
			}
			order := baseline.Order
			if test.monitor {
				expected["hami-vgpu-monitor"] = baseline.Jobs["monitor"]
				order = baseline.MonitorOrder
			}
			var actualOrder []string
			for _, job := range config.Jobs {
				name, ok := job["job_name"].(string)
				require.True(t, ok)
				actualOrder = append(actualOrder, name)
				if want, changed := expected[name]; changed {
					assert.Equal(t, want, job, name)
				}
			}
			require.Equal(t, order, actualOrder)
		})
	}
}
