package metrics

import (
	"bytes"
	"text/template"
)

const kubernetesVMAgentConfigTemplateText = `global:
  scrape_interval: 30s # Set the scrape interval to every 30 seconds. Default is every 1 minute.

scrape_configs:
# Scrape from Kubernetes pods using service discovery
- job_name: 'neutree-router'
  kubernetes_sd_configs:
  - role: pod
    namespaces:
      names:
      - {{ .Namespace }}
    selectors:
    - role: pod
      label: app=router
  relabel_configs:
  # Only scrape pods with cluster and workspace labels matching
  - source_labels: [__meta_kubernetes_pod_label_cluster]
    action: keep
    regex: {{ .ClusterName }}
  - source_labels: [__meta_kubernetes_pod_label_workspace]
    action: keep
    regex: {{ .Workspace }}
  # Set the __address__ to pod IP and port 8000
  - source_labels: [__meta_kubernetes_pod_ip]
    action: replace
    target_label: __address__
    regex: (.+)
    replacement: $1:8000
  # Add pod metadata as labels
  - source_labels: [__meta_kubernetes_namespace]
    action: replace
    target_label: namespace
  - source_labels: [__meta_kubernetes_pod_label_cluster]
    action: replace
    target_label: neutree_cluster
  - source_labels: [__meta_kubernetes_pod_label_workspace]
    action: replace
    target_label: workspace
  - source_labels: [__meta_kubernetes_pod_label_app]
    action: replace
    target_label: app
  - source_labels: [__meta_kubernetes_pod_name]
    action: replace
    target_label: pod
  # Add fixed labels to all scraped metrics
  - target_label: deployment
    replacement: Router
- job_name: 'neutree-inference'
  kubernetes_sd_configs:
  - role: pod
    namespaces:
      names:
      - {{ .Namespace }}
    selectors:
    - role: pod
      label: app=inference
  relabel_configs:
  # Only scrape pods with cluster and workspace labels matching
  - source_labels: [__meta_kubernetes_pod_label_cluster]
    action: keep
    regex: {{ .ClusterName }}
  - source_labels: [__meta_kubernetes_pod_label_workspace]
    action: keep
    regex: {{ .Workspace }}
  # Set the __address__ to pod IP and port 8000
  - source_labels: [__meta_kubernetes_pod_ip]
    action: replace
    target_label: __address__
    regex: (.+)
    replacement: $1:8000
  # Add pod metadata as labels
  - source_labels: [__meta_kubernetes_namespace]
    action: replace
    target_label: namespace
  - source_labels: [__meta_kubernetes_pod_label_cluster]
    action: replace
    target_label: neutree_cluster
  - source_labels: [__meta_kubernetes_pod_label_workspace]
    action: replace
    target_label: workspace
  - source_labels: [__meta_kubernetes_pod_label_endpoint]
    action: replace
    target_label: application
  # Add fixed labels to all scraped metrics
  - target_label: deployment
    replacement: Backend
  - source_labels: [__meta_kubernetes_pod_name]
    action: replace
    target_label: replica
  - source_labels: [__meta_kubernetes_pod_label_engine]
    action: replace
    target_label: engine
  - source_labels: [__meta_kubernetes_pod_label_engine_version]
    action: replace
    target_label: engine_version
  metric_relabel_configs:
  - source_labels: [__name__]
    regex: 'sglang[:_](.+)'
    target_label: __name__
    replacement: 'sglang:$1'
{{ if .EnableNodeExporter }}
# Scrape node-exporter metrics from all nodes (HTTP - without kube-rbac-proxy)
- job_name: 'node-exporter-http'
  kubernetes_sd_configs:
  - role: node
  # Use HTTP scheme for direct node-exporter access
  scheme: http
  relabel_configs:
  # Set the __address__ to node IP and node-exporter port.
  - source_labels: [__address__]
    action: replace
    target_label: __address__
    regex: '([^:]+)(?::\d+)?'
    replacement: '$1:{{ .NodeExporterPort }}'
  # Use node name as instance label
  - source_labels: [__meta_kubernetes_node_name]
    action: replace
    target_label: instance
  # Add node name as additional label
  - source_labels: [__meta_kubernetes_node_name]
    action: replace
    target_label: node
  # Add cluster and workspace labels
  - target_label: neutree_cluster
    replacement: {{ .ClusterName }}
  - target_label: workspace
    replacement: {{ .Workspace }}
{{ end }}
{{ range .AcceleratorExporters }}
# Scrape accelerator exporter metrics from detected accelerator nodes.
- job_name: '{{ .JobName }}'
{{ if .HasCustomMetricsPath }}
  metrics_path: {{ .MetricsPath }}
{{ end }}
  kubernetes_sd_configs:
  - role: pod
    namespaces:
      names:
      - {{ $.Namespace }}
    selectors:
    - role: pod
      label: app={{ .AppLabel }}
  relabel_configs:
  - source_labels: [__meta_kubernetes_pod_ip]
    action: replace
    target_label: __address__
    regex: (.+)
    replacement: $1:{{ .Port }}
  - source_labels: [__meta_kubernetes_pod_node_name]
    action: replace
    target_label: node
  - source_labels: [__meta_kubernetes_namespace]
    action: replace
    target_label: namespace
  - source_labels: [__meta_kubernetes_pod_name]
    action: replace
    target_label: pod
  - target_label: neutree_cluster
    replacement: {{ $.ClusterName }}
  - target_label: workspace
    replacement: {{ $.Workspace }}
{{ end }}
{{ if .EnableExternalDCGMScrape }}
# Scrape an existing dcgm-exporter deployed outside Neutree ownership.
- job_name: 'dcgm-exporter'
  kubernetes_sd_configs:
  - role: pod
    selectors:
    - role: pod
      label: app=nvidia-dcgm-exporter
  relabel_configs:
  - source_labels: [__meta_kubernetes_pod_ip]
    action: replace
    target_label: __address__
    regex: (.+)
    replacement: $1:9400
  - source_labels: [__meta_kubernetes_pod_node_name]
    action: replace
    target_label: node
  - source_labels: [__meta_kubernetes_namespace]
    action: replace
    target_label: namespace
  - source_labels: [__meta_kubernetes_pod_name]
    action: replace
    target_label: pod
  - target_label: neutree_cluster
    replacement: {{ .ClusterName }}
  - target_label: workspace
    replacement: {{ .Workspace }}
{{ end }}
{{ if .EnableHAMiMonitorScrape }}
# Scrape HAMi vGPU monitor metrics from the managed HAMi device-plugin pods
- job_name: 'hami-vgpu-monitor'
  kubernetes_sd_configs:
  - role: pod
    namespaces:
      names:
      - {{ .Namespace }}
    selectors:
    - role: pod
      label: app.kubernetes.io/component=hami-device-plugin
  relabel_configs:
  # Set the __address__ to pod IP and vGPU monitor port
  - source_labels: [__meta_kubernetes_pod_ip]
    action: replace
    target_label: __address__
    regex: (.+)
    replacement: $1:9394
  # Add monitor target metadata as labels. HAMi container metrics already
  # expose workload namespace/pod labels, so keep those intact.
  - source_labels: [__meta_kubernetes_pod_node_name]
    action: replace
    target_label: node
  - source_labels: [__meta_kubernetes_namespace]
    action: replace
    target_label: monitor_namespace
  - source_labels: [__meta_kubernetes_pod_name]
    action: replace
    target_label: monitor_pod
  # Add cluster and workspace labels
  - target_label: neutree_cluster
    replacement: {{ .ClusterName }}
  - target_label: workspace
    replacement: {{ .Workspace }}
{{ end }}
{{ if .EnableKubeStateMetrics }}
# Scrape kube-state-metrics for Neutree pod ownership labels.
- job_name: 'neutree-kube-state-metrics'
  kubernetes_sd_configs:
  - role: pod
    namespaces:
      names:
      - {{ .Namespace }}
    selectors:
    - role: pod
      label: app=neutree-kube-state-metrics
  relabel_configs:
  - source_labels: [__meta_kubernetes_pod_container_port_name]
    action: keep
    regex: http-metrics
  - source_labels: [__meta_kubernetes_pod_ip]
    action: replace
    target_label: __address__
    regex: (.+)
    replacement: $1:8080
  - source_labels: [__meta_kubernetes_namespace]
    action: replace
    target_label: monitor_namespace
  - source_labels: [__meta_kubernetes_pod_name]
    action: replace
    target_label: monitor_pod
  - target_label: neutree_cluster
    replacement: {{ .ClusterName }}
  - target_label: workspace
    replacement: {{ .Workspace }}
{{ end }}`

var kubernetesVMAgentConfigTemplate = template.Must(
	template.New("kubernetes-vmagent-config").Parse(kubernetesVMAgentConfigTemplateText),
)

func renderKubernetesVMAgentConfig(variables MetricsManifestVariables) (string, error) {
	var output bytes.Buffer
	if err := kubernetesVMAgentConfigTemplate.Execute(&output, variables); err != nil {
		return "", err
	}

	return output.String(), nil
}
