package manifests

const KubernetesVMAgentConfigTemplate = `global:
  scrape_interval: 30s # Set the scrape interval to every 30 seconds. Default is every 1 minute.
  scrape_timeout: 30s

scrape_configs:
{{ template "scrapeTargets" .BaseScrapeTargets }}
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
    regex: {{ .ClusterName | quote }}
  - source_labels: [__meta_kubernetes_pod_label_workspace]
    action: keep
    regex: {{ .Workspace | quote }}
{{- if .InferenceScrapeRules.ExclusionRegex }}
  # Drop engine versions that declared they do not export metrics. This is an
  # exclusion list, not an allow list: an engine version that declares nothing
  # is absent here and keeps being scraped, exactly as before engines could
  # declare capabilities.
  - source_labels: [__meta_kubernetes_pod_label_engine, __meta_kubernetes_pod_label_engine_version]
    action: drop
    regex: {{ .InferenceScrapeRules.ExclusionRegex | quote }}
{{- end }}
  # Set the __address__ to pod IP and port {{ .InferenceDefaultPort }}
  - source_labels: [__meta_kubernetes_pod_ip]
    action: replace
    target_label: __address__
    regex: (.+)
    replacement: $1:{{ .InferenceDefaultPort }}
{{- range .InferenceScrapeRules.Overrides }}
  # {{ .Engine }} {{ .Version }} declared a non-default metrics endpoint. These
  # rules come after the default above, so they win for the pods they match.
{{- if .HasPort }}
  - source_labels: [__meta_kubernetes_pod_ip, __meta_kubernetes_pod_label_engine, __meta_kubernetes_pod_label_engine_version]
    action: replace
    target_label: __address__
    regex: (.+);{{ .TargetRegex }}
    replacement: $1:{{ .Port }}
{{- end }}
{{- if .HasPath }}
  - source_labels: [__meta_kubernetes_pod_label_engine, __meta_kubernetes_pod_label_engine_version]
    action: replace
    target_label: __metrics_path__
    regex: {{ .TargetRegex | quote }}
    replacement: {{ .Path | quote }}
{{- end }}
{{- end }}
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
  - source_labels: [__meta_kubernetes_pod_label_endpoint]
    action: replace
    target_label: endpoint
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
{{ .CacheScrapeConfig }}
{{ template "scrapeTargets" .ScrapeTargets }}
{{ define "scrapeTargets" }}{{ range . }}
# Scrape a registered endpoint declaration.
- job_name: {{ .JobName | quote }}
{{ if and .MetricsPath (ne .MetricsPath "/metrics") }}
  metrics_path: {{ .MetricsPath | quote }}
{{ end }}
{{ if .StaticTargets }}
  static_configs:
  - targets:
{{ .StaticTargets | toYaml | indent 4 }}
{{ else }}
  kubernetes_sd_configs:
  - role: {{ .KubernetesRole | default "pod" }}
{{ if .Namespace }}
    namespaces:
      names:
      - {{ .Namespace | quote }}
{{ end }}
{{ if .Selectors }}
    selectors:
{{ range .Selectors }}
    - role: {{ .Role | quote }}
{{ if .Label }}
      label: {{ .Label | quote }}
{{ end }}
{{ if .Field }}
      field: {{ .Field | quote }}
{{ end }}
{{ end }}
{{ end }}
{{ end }}
{{ if .Scheme }}
  scheme: {{ .Scheme | quote }}
{{ end }}
{{ if .RelabelConfigs }}
  relabel_configs:
{{ .RelabelConfigs | toYaml | indent 2 }}
{{ end }}
{{ end }}{{ end }}`
