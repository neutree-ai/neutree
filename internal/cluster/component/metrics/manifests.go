package metrics

import (
	"strings"

	"github.com/neutree-ai/neutree/internal/componentversion"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/internal/util"
)

const MinKubeStateMetricsClusterVersion = "v1.1.0"

var metricsManifestTemplate = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: vmagent-config
  namespace: {{ .Namespace }}
  labels:
    app: vmagent
data:
  prometheus.yml: |
    global:
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
    # Scrape node-exporter metrics from all nodes (HTTPS - with kube-rbac-proxy)
    - job_name: 'node-exporter-https'
      kubernetes_sd_configs:
      - role: node
      # Use bearer token for authentication with kube-rbac-proxy
      bearer_token_file: /var/run/secrets/kubernetes.io/serviceaccount/token
      # Use HTTPS scheme
      scheme: https
      # Skip TLS verification for self-signed certificates
      tls_config:
        insecure_skip_verify: true
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
    # Scrape Neutree normalized node and accelerator metrics.
    - job_name: 'neutree-node-agent'
      kubernetes_sd_configs:
      - role: pod
        namespaces:
          names:
          - {{ .Namespace }}
        selectors:
        - role: pod
          label: app={{ .NeutreeMetricsName }}
      relabel_configs:
      - source_labels: [__meta_kubernetes_pod_ip]
        action: replace
        target_label: __address__
        regex: (.+)
        replacement: $1:{{ .NeutreeMetricsPort }}
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
{{ end }}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: vmagent-service-account
  namespace: {{ .Namespace }}
  labels:
    app: vmagent
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: vmagent-pod-reader
  namespace: {{ .Namespace }}
  labels:
    app: vmagent
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
subjects:
- kind: ServiceAccount
  name: vmagent-service-account
  namespace: {{ .Namespace }}
roleRef:
  kind: Role
  name: vmagent-pod-reader
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: vmagent-node-reader-{{ .HashSuffix }}
  labels:
    app: vmagent
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
rules:
- apiGroups: [""]
  resources: ["nodes", "nodes/metrics", "nodes/proxy"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch"]
- nonResourceURLs:
  - /metrics
  verbs:
  - get
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: vmagent-node-reader-{{ .HashSuffix }}
  labels:
    app: vmagent
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
subjects:
- kind: ServiceAccount
  name: vmagent-service-account
  namespace: {{ .Namespace }}
roleRef:
  kind: ClusterRole
  name: vmagent-node-reader-{{ .HashSuffix }}
  apiGroup: rbac.authorization.k8s.io
{{ if .EnableKubeStateMetrics }}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: neutree-kube-state-metrics
  namespace: {{ .Namespace }}
  labels:
    app: neutree-kube-state-metrics
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: neutree-kube-state-metrics
  namespace: {{ .Namespace }}
  labels:
    app: neutree-kube-state-metrics
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: neutree-kube-state-metrics
  namespace: {{ .Namespace }}
  labels:
    app: neutree-kube-state-metrics
subjects:
- kind: ServiceAccount
  name: neutree-kube-state-metrics
  namespace: {{ .Namespace }}
roleRef:
  kind: Role
  name: neutree-kube-state-metrics
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: v1
kind: Service
metadata:
  name: neutree-kube-state-metrics
  namespace: {{ .Namespace }}
  labels:
    app: neutree-kube-state-metrics
spec:
  type: ClusterIP
  selector:
    app: neutree-kube-state-metrics
  ports:
  - name: http-metrics
    port: 8080
    targetPort: http-metrics
  - name: telemetry
    port: 8081
    targetPort: telemetry
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: neutree-kube-state-metrics
  namespace: {{ .Namespace }}
  labels:
    app: neutree-kube-state-metrics
    neutree.ai/cluster-version: {{ .ClusterVersion }}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: neutree-kube-state-metrics
  template:
    metadata:
      labels:
        app: neutree-kube-state-metrics
        cluster: {{ .ClusterName }}
        workspace: {{ .Workspace }}
        neutree.ai/cluster-version: {{ .ClusterVersion }}
    spec:
      imagePullSecrets:
      - name: {{ .ImagePullSecret }}
      serviceAccountName: neutree-kube-state-metrics
      containers:
      - name: kube-state-metrics
        image: {{ .ImagePrefix }}/kube-state-metrics/kube-state-metrics:{{ .KubeStateMetricsVersion }}
        args:
        - --port=8080
        - --telemetry-port=8081
        - --resources=pods
        - --namespaces={{ .Namespace }}
        - --metric-labels-allowlist=pods=[app,cluster,workspace,endpoint,engine,engine_version]
        ports:
        - name: http-metrics
          containerPort: 8080
        - name: telemetry
          containerPort: 8081
        resources:
          limits:
            {{- range $key, $value := .KubeStateMetricsResources }}
            {{ $key }}: {{ $value }}
            {{- end }}
          requests:
            {{- range $key, $value := .KubeStateMetricsResources }}
            {{ $key }}: {{ $value }}
            {{- end }}
{{ end }}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ .NeutreeMetricsName }}
  namespace: {{ .Namespace }}
  labels:
    app: {{ .NeutreeMetricsName }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ .NeutreeMetricsName }}-{{ .HashSuffix }}
  labels:
    app: {{ .NeutreeMetricsName }}
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
rules:
- apiGroups: [""]
  resources: ["nodes"]
  verbs: ["get", "list", "watch", "patch"]
- apiGroups: [""]
  resources: ["nodes/proxy"]
  verbs: ["get"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ .NeutreeMetricsName }}-{{ .HashSuffix }}
  labels:
    app: {{ .NeutreeMetricsName }}
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
subjects:
- kind: ServiceAccount
  name: {{ .NeutreeMetricsName }}
  namespace: {{ .Namespace }}
roleRef:
  kind: ClusterRole
  name: {{ .NeutreeMetricsName }}-{{ .HashSuffix }}
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: {{ .NodeExporterName }}
  namespace: {{ .Namespace }}
  labels:
    app: {{ .NodeExporterName }}
    neutree.ai/cluster-version: {{ .ClusterVersion }}
spec:
  selector:
    matchLabels:
      app: {{ .NodeExporterName }}
      cluster: {{ .ClusterName }}
      workspace: {{ .Workspace }}
  template:
    metadata:
      labels:
        app: {{ .NodeExporterName }}
        cluster: {{ .ClusterName }}
        workspace: {{ .Workspace }}
        neutree.ai/cluster-version: {{ .ClusterVersion }}
    spec:
      hostNetwork: true
      hostPID: true
      tolerations:
      - operator: Exists
      imagePullSecrets:
      - name: {{ .ImagePullSecret }}
      containers:
      - name: node-exporter
        image: {{ .NodeExporterImage }}
        args:
        - --path.rootfs=/host
        - --web.listen-address=:{{ .NodeExporterPort }}
        ports:
        - name: metrics
          containerPort: {{ .NodeExporterPort }}
        volumeMounts:
        - name: host-root
          mountPath: /host
          readOnly: true
          mountPropagation: HostToContainer
      volumes:
      - name: host-root
        hostPath:
          path: /
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: {{ .NeutreeMetricsName }}
  namespace: {{ .Namespace }}
  labels:
    app: {{ .NeutreeMetricsName }}
    neutree.ai/cluster-version: {{ .ClusterVersion }}
spec:
  selector:
    matchLabels:
      app: {{ .NeutreeMetricsName }}
      cluster: {{ .ClusterName }}
      workspace: {{ .Workspace }}
  template:
    metadata:
      labels:
        app: {{ .NeutreeMetricsName }}
        cluster: {{ .ClusterName }}
        workspace: {{ .Workspace }}
        neutree.ai/cluster-version: {{ .ClusterVersion }}
    spec:
      serviceAccountName: {{ .NeutreeMetricsName }}
      hostNetwork: true
      tolerations:
      - operator: Exists
      imagePullSecrets:
      - name: {{ .ImagePullSecret }}
      containers:
      - name: neutree-node-agent
        image: {{ .NeutreeMetricsImage }}
        args:
        - --listen-address=:{{ .NeutreeMetricsPort }}
        - --cluster={{ .ClusterName }}
        - --workspace={{ .Workspace }}
        - --cluster-type=kubernetes
        - --node=$(NODE_NAME)
        - --node-ip=$(NODE_IP)
        - --node-exporter-url=http://127.0.0.1:{{ .NodeExporterPort }}/metrics
        - --kubelet-pod-resources-socket={{ .KubeletPodResourcesSocket }}
        - --enable-kubernetes-annotation-writer
{{ range .NeutreeMetricsAcceleratorExporterURLs }}
        - --accelerator-exporter-url={{ . }}
{{ end }}
        env:
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: NODE_IP
          valueFrom:
            fieldRef:
              fieldPath: status.hostIP
        ports:
        - name: metrics
          containerPort: {{ .NeutreeMetricsPort }}
        livenessProbe:
          httpGet:
            path: /health
            port: metrics
          initialDelaySeconds: 10
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /health
            port: metrics
          initialDelaySeconds: 5
          periodSeconds: 10
        volumeMounts:
        - name: kubelet-pod-resources
          mountPath: /var/lib/kubelet/pod-resources
        resources:
          limits:
            {{- range $key, $value := .NeutreeMetricsResources }}
            {{ $key }}: {{ $value }}
            {{- end }}
          requests:
            {{- range $key, $value := .NeutreeMetricsResources }}
            {{ $key }}: {{ $value }}
            {{- end }}
      volumes:
      - name: kubelet-pod-resources
        hostPath:
          path: /var/lib/kubelet/pod-resources
          type: DirectoryOrCreate
{{ range .AcceleratorExporters }}
{{ if .ConfigFileData }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .ConfigMapName }}
  namespace: {{ $.Namespace }}
  labels:
    app: {{ .AppLabel }}
    cluster: {{ $.ClusterName }}
    workspace: {{ $.Workspace }}
    neutree.ai/cluster-version: {{ $.ClusterVersion }}
data:
{{ .ConfigFileData | toYaml | indent 2 }}
{{ end }}
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: {{ .Name }}
  namespace: {{ $.Namespace }}
  labels:
    app: {{ .AppLabel }}
    neutree.ai/cluster-version: {{ $.ClusterVersion }}
spec:
  selector:
    matchLabels:
      app: {{ .AppLabel }}
      cluster: {{ $.ClusterName }}
      workspace: {{ $.Workspace }}
  template:
    metadata:
      labels:
        app: {{ .AppLabel }}
        cluster: {{ $.ClusterName }}
        workspace: {{ $.Workspace }}
        neutree.ai/cluster-version: {{ $.ClusterVersion }}
    spec:
      hostNetwork: {{ .HostNetwork }}
      hostPID: {{ .HostPID }}
      tolerations:
      - operator: Exists
{{ if .NodeSelector }}
      nodeSelector:
{{ .NodeSelector | toYaml | indent 8 }}
{{ end }}
      imagePullSecrets:
      - name: {{ $.ImagePullSecret }}
      containers:
      - name: {{ .ContainerName }}
        image: {{ .Image }}
{{ if .Args }}
        args:
{{ .Args | toYaml | indent 8 }}
{{ end }}
{{ if .Env }}
        env:
{{ .Env | toYaml | indent 8 }}
{{ end }}
        ports:
        - name: metrics
          containerPort: {{ .Port }}
{{ if .Capabilities }}
        securityContext:
          capabilities:
            add:
{{ .Capabilities | toYaml | indent 12 }}
{{ end }}
{{ if .VolumeMounts }}
        volumeMounts:
{{ .VolumeMounts | toYaml | indent 8 }}
{{ end }}
{{ if .Volumes }}
      volumes:
{{ .Volumes | toYaml | indent 6 }}
{{ end }}
{{ end }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vmagent
  namespace: {{ .Namespace }}
  labels:
    app: vmagent
    neutree.ai/cluster-version: {{ .ClusterVersion }}
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
        neutree.ai/cluster-version: {{ .ClusterVersion }}
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
        image: {{ .ImagePrefix }}/victoriametrics/vmagent:{{ .Version }}
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
	ClusterName                           string
	Workspace                             string
	Namespace                             string
	ImagePrefix                           string
	ImagePullSecret                       string
	Version                               string
	NodeExporterName                      string
	NodeExporterImage                     string
	NodeExporterPort                      int
	NeutreeMetricsName                    string
	NeutreeMetricsImage                   string
	NeutreeMetricsPort                    int
	KubeletPodResourcesSocket             string
	KubeStateMetricsVersion               string
	ClusterVersion                        string
	MetricsRemoteWriteURL                 string
	Replicas                              int
	Resources                             map[string]string
	NeutreeMetricsResources               map[string]string
	KubeStateMetricsResources             map[string]string
	HashSuffix                            string
	EnableHAMiMonitorScrape               bool
	EnableKubeStateMetrics                bool
	AcceleratorExporters                  []metricsAcceleratorExporter
	NeutreeMetricsAcceleratorExporterURLs []string
}

// buildManifestVariables creates the data structure for rendering manifests
func (m *MetricsComponent) buildManifestVariables() MetricsManifestVariables {
	// Default values for metrics component
	version := componentversion.VictoriaMetrics
	replicas := 1
	resources := map[string]string{
		"cpu":    "100m",
		"memory": "256Mi",
	}
	kubeStateMetricsResources := map[string]string{
		"cpu":    "100m",
		"memory": "128Mi",
	}
	neutreeMetricsResources := map[string]string{
		"cpu":    "100m",
		"memory": "128Mi",
	}

	return MetricsManifestVariables{
		ClusterName:               m.cluster.Metadata.Name,
		Workspace:                 m.cluster.Metadata.Workspace,
		Namespace:                 m.namespace,
		ImagePrefix:               m.imagePrefix,
		ImagePullSecret:           m.imagePullSecret,
		Version:                   version,
		NodeExporterName:          nodeExporterDaemonSetName,
		NodeExporterImage:         rewriteMetricsImage(m.imagePrefix, defaultNodeExporterImage),
		NodeExporterPort:          nodeExporterPort,
		NeutreeMetricsName:        neutreeMetricsName,
		NeutreeMetricsImage:       m.imagePrefix + "/neutree/neutree-node-agent:" + componentversion.NeutreeNodeAgent,
		NeutreeMetricsPort:        neutreeMetricsPort,
		KubeletPodResourcesSocket: "/var/lib/kubelet/pod-resources/kubelet.sock",
		KubeStateMetricsVersion:   componentversion.KubeStateMetrics,
		ClusterVersion:            m.cluster.GetVersion(),
		MetricsRemoteWriteURL:     m.metricsRemoteWriteURL,
		Replicas:                  replicas,
		Resources:                 resources,
		NeutreeMetricsResources:   neutreeMetricsResources,
		KubeStateMetricsResources: kubeStateMetricsResources,
		HashSuffix:                util.HashString(m.cluster.Key()),
	}
}

func (m *MetricsComponent) supportsKubeStateMetrics() (bool, error) {
	return supportsKubeStateMetricsClusterVersion(m.cluster.GetVersion())
}

func supportsKubeStateMetricsClusterVersion(version string) (bool, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return false, nil
	}

	baseVersion, err := semver.BaseVersion(version)
	if err != nil {
		return false, err
	}

	lessThanMinVersion, err := semver.LessThan(baseVersion, MinKubeStateMetricsClusterVersion)
	if err != nil {
		return false, err
	}

	return !lessThanMinVersion, nil
}
