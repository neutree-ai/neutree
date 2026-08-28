package metrics

import "github.com/neutree-ai/neutree/internal/component"

const (
	nodeExporterDaemonSetName   = "neutree-node-exporter"
	nodeExporterPort            = 19100
	neutreeNodeAgentMetricsName = "neutree-node-agent"
	neutreeNodeAgentImageName   = "neutree/neutree-node-agent"
	neutreeNodeAgentMetricsPort = 19101

	defaultNodeExporterImage     = "quay.io/prometheus/node-exporter:" + component.NodeExporter
	defaultKubeStateMetricsImage = "registry.k8s.io/kube-state-metrics/kube-state-metrics:" + component.KubeStateMetrics
	defaultVMAgentImage          = "victoriametrics/vmagent:" + component.VictoriaMetrics
	defaultMetricsPath           = "/metrics"
	acceleratorExporterJobPrefix = "accelerator-exporter"
)
