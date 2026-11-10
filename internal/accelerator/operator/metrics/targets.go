package metrics

import (
	"fmt"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promrelabel"
)

// PodRelabelConfigs builds the common Kubernetes pod scrape relabeling used by
// Legacy Metrics and accelerator Operators.
func PodRelabelConfigs(clusterName, workspace string, port int, namespaceLabel, podLabel string) []promrelabel.RelabelConfig {
	address := fmt.Sprintf("$1:%d", port)

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
