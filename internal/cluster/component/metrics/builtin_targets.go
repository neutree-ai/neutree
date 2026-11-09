package metrics

import (
	"fmt"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promrelabel"
	"k8s.io/utils/ptr"

	"github.com/neutree-ai/neutree/internal/metricsconfig"
)

func routerScrapeTarget(variables MetricsManifestVariables) metricsconfig.Target {
	return metricsconfig.Target{
		JobName: "neutree-router", Namespace: variables.Namespace, Selectors: metricsconfig.PodSelectors(map[string]string{"app": "router"}), Port: 8000,
		RelabelConfigs: []promrelabel.RelabelConfig{
			{SourceLabels: []string{"__meta_kubernetes_pod_label_cluster"}, Action: "keep", Regex: &promrelabel.MultiLineRegex{S: variables.ClusterName}},
			{SourceLabels: []string{"__meta_kubernetes_pod_label_workspace"}, Action: "keep", Regex: &promrelabel.MultiLineRegex{S: variables.Workspace}},
			{SourceLabels: []string{"__meta_kubernetes_pod_ip"}, Action: "replace", TargetLabel: "__address__",
				Regex: &promrelabel.MultiLineRegex{S: "(.+)"}, Replacement: ptr.To("$1:8000")},
			{SourceLabels: []string{"__meta_kubernetes_namespace"}, Action: "replace", TargetLabel: "namespace"},
			{SourceLabels: []string{"__meta_kubernetes_pod_label_cluster"}, Action: "replace", TargetLabel: "neutree_cluster"},
			{SourceLabels: []string{"__meta_kubernetes_pod_label_workspace"}, Action: "replace", TargetLabel: "workspace"},
			{SourceLabels: []string{"__meta_kubernetes_pod_label_app"}, Action: "replace", TargetLabel: "app"},
			{SourceLabels: []string{"__meta_kubernetes_pod_name"}, Action: "replace", TargetLabel: "pod"},
			{TargetLabel: "deployment", Replacement: ptr.To("Router")},
		},
	}
}

func nodeExporterScrapeTarget(variables MetricsManifestVariables) metricsconfig.Target {
	return metricsconfig.Target{
		JobName: "node-exporter-http", KubernetesRole: "node", Scheme: "http", Port: variables.NodeExporterPort,
		RelabelConfigs: []promrelabel.RelabelConfig{
			{SourceLabels: []string{"__address__"}, Action: "replace", TargetLabel: "__address__",
				Regex: &promrelabel.MultiLineRegex{S: `([^:]+)(?::\d+)?`}, Replacement: ptr.To(fmt.Sprintf("$1:%d", variables.NodeExporterPort))},
			{SourceLabels: []string{"__meta_kubernetes_node_name"}, Action: "replace", TargetLabel: "instance"},
			{SourceLabels: []string{"__meta_kubernetes_node_name"}, Action: "replace", TargetLabel: "node"},
			{TargetLabel: "neutree_cluster", Replacement: ptr.To(variables.ClusterName)},
			{TargetLabel: "workspace", Replacement: ptr.To(variables.Workspace)},
		},
	}
}

func stateMetricsScrapeTarget(variables MetricsManifestVariables) metricsconfig.Target {
	return metricsconfig.Target{
		JobName: "neutree-kube-state-metrics", Namespace: variables.Namespace,
		Selectors: metricsconfig.PodSelectors(map[string]string{"app": "neutree-kube-state-metrics"}), Port: 8080,
		RelabelConfigs: []promrelabel.RelabelConfig{
			{SourceLabels: []string{"__meta_kubernetes_pod_container_port_name"}, Action: "keep", Regex: &promrelabel.MultiLineRegex{S: "http-metrics"}},
			{SourceLabels: []string{"__meta_kubernetes_pod_ip"}, Action: "replace", TargetLabel: "__address__",
				Regex: &promrelabel.MultiLineRegex{S: "(.+)"}, Replacement: ptr.To("$1:8080")},
			{SourceLabels: []string{"__meta_kubernetes_namespace"}, Action: "replace", TargetLabel: "monitor_namespace"},
			{SourceLabels: []string{"__meta_kubernetes_pod_name"}, Action: "replace", TargetLabel: "monitor_pod"},
			{TargetLabel: "neutree_cluster", Replacement: ptr.To(variables.ClusterName)},
			{TargetLabel: "workspace", Replacement: ptr.To(variables.Workspace)},
		},
	}
}

func withBuiltinScrapeTargets(variables MetricsManifestVariables) MetricsManifestVariables {
	// Keep router before inference and kube-state-metrics after the selected component targets.
	variables.BaseScrapeTargets = []metricsconfig.Target{routerScrapeTarget(variables)}
	var targets []metricsconfig.Target

	if variables.EnableNodeExporter {
		targets = append(targets, nodeExporterScrapeTarget(variables))
	}

	targets = append(targets, variables.ScrapeTargets...)

	if variables.EnableKubeStateMetrics {
		targets = append(targets, stateMetricsScrapeTarget(variables))
	}

	variables.ScrapeTargets = targets

	return variables
}
