package metrics

import (
	"fmt"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promrelabel"
)

func (m *MetricsComponent) podScrapeRelabelConfigs(port int, namespaceLabel, podLabel string) []promrelabel.RelabelConfig {
	address := fmt.Sprintf("$1:%d", port)
	clusterName, workspace := m.cluster.GetName(), m.cluster.GetWorkspace()

	return []promrelabel.RelabelConfig{
		{SourceLabels: []string{"__meta_kubernetes_pod_ip"}, Action: "replace", TargetLabel: "__address__",
			Regex: &promrelabel.MultiLineRegex{S: "(.+)"}, Replacement: &address},
		{SourceLabels: []string{"__meta_kubernetes_pod_node_name"}, Action: "replace", TargetLabel: "node"},
		{SourceLabels: []string{"__meta_kubernetes_namespace"}, Action: "replace", TargetLabel: namespaceLabel},
		{SourceLabels: []string{"__meta_kubernetes_pod_name"}, Action: "replace", TargetLabel: podLabel},
		{TargetLabel: "neutree_cluster", Replacement: &clusterName},
		{TargetLabel: "workspace", Replacement: &workspace},
	}
}
