package metrics

import "github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"

var metricsManifestTemplate = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: vmagent-config
  namespace: {{ .Namespace }}
  labels:
    app: vmagent
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
data:
  prometheus.yml: |
    global:
      scrape_interval: 30s # Set the scrape interval to every 30 seconds. Default is every 1 minute.

    scrape_configs:
    # Scrape from Kubernetes pods using service discovery
    - job_name: 'neutree'
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
      # Use the metrics port from pod annotations or default to 8080
      - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_port]
        action: replace
        target_label: __address__
        regex: (.+)
        replacement: __meta_kubernetes_pod_ip:$1
      - source_labels: [__meta_kubernetes_pod_ip]
        action: replace
        target_label: __address__
        regex: (.+)
        replacement: $1:8000
      # Use the metrics path from pod annotations or default to /metrics
      - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_path]
        action: replace
        target_label: __metrics_path__
        regex: (.+)
      # Add pod metadata as labels
      - source_labels: [__meta_kubernetes_pod_name]
        action: replace
        target_label: pod
      - source_labels: [__meta_kubernetes_namespace]
        action: replace
        target_label: namespace
      - source_labels: [__meta_kubernetes_pod_label_component]
        action: replace
        target_label: component
      - source_labels: [__meta_kubernetes_pod_label_endpoint]
        action: replace
        target_label: endpoint
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: vmagent-service-account
  namespace: {{ .Namespace }}
  labels:
    app: vmagent
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: vmagent-pod-reader
  namespace: {{ .Namespace }}
  labels:
    app: vmagent
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
rules:
- apiGroups: [""]
  resources: ["pods", "endpoints", "services"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["nodes"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: vmagent-rolebinding
  namespace: {{ .Namespace }}
  labels:
    app: vmagent
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
subjects:
- kind: ServiceAccount
  name: vmagent-service-account
  namespace: {{ .Namespace }}
roleRef:
  kind: Role
  name: vmagent-pod-reader
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vmagent
  namespace: {{ .Namespace }}
  labels:
    app: vmagent
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
spec:
  replicas: {{ .Replicas }}
  selector:
    matchLabels:
      app: vmagent
      cluster: {{ .ClusterName }}
      workspace: {{ .Workspace }}
  template:
    metadata:
      labels:
        app: vmagent
        cluster: {{ .ClusterName }}
        workspace: {{ .Workspace }}
    spec:
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 100
            podAffinityTerm:
              labelSelector:
                matchExpressions:
                - key: app
                  operator: In
                  values:
                  - vmagent
              topologyKey: "kubernetes.io/hostname"
      imagePullSecrets:
      - name: {{ .ImagePullSecret }}
      serviceAccountName: vmagent-service-account
      containers:
      - name: vmagent
        image: {{ .ImagePrefix }}/vmagent:{{ .Version }}
        args:
        - --promscrape.config=/etc/prometheus/prometheus.yml
        - --promscrape.configCheckInterval=10s
        - --remoteWrite.url={{ .MetricsRemoteWriteURL }}
        resources:
          limits:
            {{- range $key, $value := .Resources }}
            {{ $key }}: {{ $value }}
            {{- end }}
          requests:
            {{- range $key, $value := .Resources }}
            {{ $key }}: {{ $value }}
            {{- end }}
        volumeMounts:
        - name: vmagent-config
          mountPath: /etc/prometheus
      volumes:
      - name: vmagent-config
        configMap:
          name: vmagent-config
`

// MetricsManifestVariables holds the variables for rendering metrics manifests
type MetricsManifestVariables struct {
	ClusterName           string
	Workspace             string
	Namespace             string
	ImagePrefix           string
	ImagePullSecret       string
	Version               string
	MetricsRemoteWriteURL string
	Replicas              int
	Resources             map[string]string
}

// buildManifestVariables creates the data structure for rendering manifests
func (m *MetricsComponent) buildManifestVariables() MetricsManifestVariables {
	// Default values for metrics component
	version := constants.VictoriaMetricsVersion
	replicas := 1
	resources := map[string]string{
		"cpu":    "100m",
		"memory": "256Mi",
	}

	return MetricsManifestVariables{
		ClusterName:           m.cluster.Metadata.Name,
		Workspace:             m.cluster.Metadata.Workspace,
		Namespace:             m.namespace,
		ImagePrefix:           m.imagePrefix,
		ImagePullSecret:       m.imagePullSecret,
		Version:               version,
		MetricsRemoteWriteURL: m.metricsRemoteWriteURL,
		Replicas:              replicas,
		Resources:             resources,
	}
}
