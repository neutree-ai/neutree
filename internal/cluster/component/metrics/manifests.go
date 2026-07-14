package metrics

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/neutree-ai/neutree/internal/componentversion"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/internal/util"
)

const (
	MinKubeStateMetricsClusterVersion = "v1.1.0"
	// Clusters before v1.1.0 do not support Neutree-managed metrics exporters.
	MinManagedMetricsExporterClusterVersion = "v1.1.0"
)

const (
	vmagentRBACMetricsManifestTemplate = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: vmagent-config
  namespace: {{ .Namespace }}
  labels:
    app: vmagent
data:
  prometheus.yml: |
{{ .VMAgentConfig | indent 4 }}
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
`

	kubeStateMetricsManifestTemplate = `
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
`

	neutreeNodeAgentRBACMetricsManifestTemplate = `
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ .NeutreeNodeAgentMetricsName }}
  namespace: {{ .Namespace }}
  labels:
    app: {{ .NeutreeNodeAgentMetricsName }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ .NeutreeNodeAgentMetricsName }}-{{ .HashSuffix }}
  labels:
    app: {{ .NeutreeNodeAgentMetricsName }}
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
  name: {{ .NeutreeNodeAgentMetricsName }}-{{ .HashSuffix }}
  labels:
    app: {{ .NeutreeNodeAgentMetricsName }}
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
subjects:
- kind: ServiceAccount
  name: {{ .NeutreeNodeAgentMetricsName }}
  namespace: {{ .Namespace }}
roleRef:
  kind: ClusterRole
  name: {{ .NeutreeNodeAgentMetricsName }}-{{ .HashSuffix }}
  apiGroup: rbac.authorization.k8s.io
`

	nodeExporterMetricsManifestTemplate = `
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
      volumes:
      - name: host-root
        hostPath:
          path: /
`

	neutreeNodeAgentDaemonSetMetricsManifestTemplate = `
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: {{ .NeutreeNodeAgentMetricsName }}
  namespace: {{ .Namespace }}
  labels:
    app: {{ .NeutreeNodeAgentMetricsName }}
    neutree.ai/cluster-version: {{ .ClusterVersion }}
spec:
  selector:
    matchLabels:
      app: {{ .NeutreeNodeAgentMetricsName }}
      cluster: {{ .ClusterName }}
      workspace: {{ .Workspace }}
  template:
    metadata:
      labels:
        app: {{ .NeutreeNodeAgentMetricsName }}
        cluster: {{ .ClusterName }}
        workspace: {{ .Workspace }}
        neutree.ai/cluster-version: {{ .ClusterVersion }}
    spec:
      serviceAccountName: {{ .NeutreeNodeAgentMetricsName }}
      tolerations:
      - operator: Exists
      imagePullSecrets:
      - name: {{ .ImagePullSecret }}
      containers:
      - name: neutree-node-agent
        image: {{ .NeutreeNodeAgentMetricsImage }}
        args:
        - --listen-address=:{{ .NeutreeNodeAgentMetricsPort }}
        - --cluster-type=kubernetes
        - --metrics-mode={{ .MetricsMode }}
        - --node=$(NODE_NAME)
        - --node-ip=$(NODE_IP)
        env:
{{ if .NeutreeNodeAgentMetricsEnv }}
{{ .NeutreeNodeAgentMetricsEnv | toYaml | indent 8 }}
{{ end }}
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
          containerPort: {{ .NeutreeNodeAgentMetricsPort }}
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
            {{- range $key, $value := .NeutreeNodeAgentMetricsResources }}
            {{ $key }}: {{ $value }}
            {{- end }}
          requests:
            {{- range $key, $value := .NeutreeNodeAgentMetricsResources }}
            {{ $key }}: {{ $value }}
            {{- end }}
      volumes:
      - name: kubelet-pod-resources
        hostPath:
          path: /var/lib/kubelet/pod-resources
          type: DirectoryOrCreate
`

	acceleratorExporterMetricsManifestTemplate = `
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
    neutree.ai/metrics-target: accelerator-exporter
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
        neutree.ai/metrics-target: accelerator-exporter
        cluster: {{ $.ClusterName }}
        workspace: {{ $.Workspace }}
        neutree.ai/cluster-version: {{ $.ClusterVersion }}
{{ if .ConfigChecksum }}
      annotations:
        checksum/config: {{ .ConfigChecksum }}
{{ end }}
    spec:
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
`

	vmagentDeploymentMetricsManifestTemplate = `
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
)

// MetricsManifestVariables holds the variables for rendering metrics manifests
type MetricsManifestVariables struct {
	ClusterName                      string
	Workspace                        string
	Namespace                        string
	ImagePrefix                      string
	ImagePullSecret                  string
	Version                          string
	NodeExporterName                 string
	NodeExporterImage                string
	NodeExporterPort                 int
	NeutreeNodeAgentMetricsName      string
	NeutreeNodeAgentMetricsImage     string
	NeutreeNodeAgentMetricsPort      int
	NeutreeNodeAgentMetricsEnv       []corev1.EnvVar
	KubeStateMetricsVersion          string
	ClusterVersion                   string
	MetricsRemoteWriteURL            string
	MetricsMode                      string
	Replicas                         int
	Resources                        map[string]string
	NeutreeNodeAgentMetricsResources map[string]string
	KubeStateMetricsResources        map[string]string
	HashSuffix                       string
	EnableHAMiMonitorScrape          bool
	EnableVMAgent                    bool
	EnableKubeStateMetrics           bool
	EnableNeutreeNodeAgentMetrics    bool
	EnableNodeExporter               bool
	EnableExternalDCGMScrape         bool
	AcceleratorExporters             []metricsAcceleratorExporter
	VMAgentConfig                    string
}

func buildMetricsManifestTemplate(variables MetricsManifestVariables) string {
	var templates []string

	if variables.EnableVMAgent {
		templates = append(templates,
			vmagentRBACMetricsManifestTemplate,
			vmagentDeploymentMetricsManifestTemplate,
		)
	}

	if variables.EnableKubeStateMetrics {
		templates = append(templates, kubeStateMetricsManifestTemplate)
	}

	if variables.EnableNeutreeNodeAgentMetrics {
		templates = append(templates,
			neutreeNodeAgentRBACMetricsManifestTemplate,
			neutreeNodeAgentDaemonSetMetricsManifestTemplate,
		)
	}

	if variables.EnableNodeExporter {
		templates = append(templates, nodeExporterMetricsManifestTemplate)
	}

	if len(variables.AcceleratorExporters) > 0 {
		templates = append(templates, acceleratorExporterMetricsManifestTemplate)
	}

	return strings.Join(templates, "\n")
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
	neutreeNodeAgentMetricsResources := map[string]string{
		"cpu":    "500m",
		"memory": "128Mi",
	}

	return MetricsManifestVariables{
		ClusterName:                      m.cluster.Metadata.Name,
		Workspace:                        m.cluster.Metadata.Workspace,
		Namespace:                        m.namespace,
		ImagePrefix:                      m.imagePrefix,
		ImagePullSecret:                  m.imagePullSecret,
		Version:                          version,
		NodeExporterName:                 nodeExporterDaemonSetName,
		NodeExporterImage:                util.RewriteImageRef(m.imagePrefix, defaultNodeExporterImage),
		NodeExporterPort:                 nodeExporterPort,
		NeutreeNodeAgentMetricsName:      neutreeNodeAgentMetricsName,
		NeutreeNodeAgentMetricsImage:     util.RewriteImageRef(m.imagePrefix, neutreeNodeAgentImageName+":"+componentversion.NeutreeNodeAgent),
		NeutreeNodeAgentMetricsPort:      neutreeNodeAgentMetricsPort,
		KubeStateMetricsVersion:          componentversion.KubeStateMetrics,
		ClusterVersion:                   m.cluster.GetVersion(),
		MetricsRemoteWriteURL:            m.metricsRemoteWriteURL,
		MetricsMode:                      string(m.acceleratorExporterMode()),
		Replicas:                         replicas,
		Resources:                        resources,
		NeutreeNodeAgentMetricsResources: neutreeNodeAgentMetricsResources,
		KubeStateMetricsResources:        kubeStateMetricsResources,
		HashSuffix:                       util.HashString(m.cluster.Key()),
	}
}

func (m *MetricsComponent) supportsKubeStateMetrics() (bool, error) {
	return supportsClusterVersionAtLeast(m.cluster.GetVersion(), MinKubeStateMetricsClusterVersion)
}

func (m *MetricsComponent) supportsManagedMetricsExporters() (bool, error) {
	return supportsClusterVersionAtLeast(m.cluster.GetVersion(), MinManagedMetricsExporterClusterVersion)
}

func supportsClusterVersionAtLeast(version string, minVersion string) (bool, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return false, nil
	}

	baseVersion, err := semver.BaseVersion(version)
	if err != nil {
		return false, err
	}

	lessThanMinVersion, err := semver.LessThan(baseVersion, minVersion)
	if err != nil {
		return false, err
	}

	return !lessThanMinVersion, nil
}
