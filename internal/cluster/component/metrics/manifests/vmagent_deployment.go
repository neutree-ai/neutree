package manifests

const vmagentPrerequisitesManifestTemplate = `
{{ if .EnableVMAgent }}
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
    cluster: {{ .ClusterName | quote }}
    workspace: {{ .Workspace | quote }}
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
    cluster: {{ .ClusterName | quote }}
    workspace: {{ .Workspace | quote }}
subjects:
- kind: ServiceAccount
  name: vmagent-service-account
  namespace: {{ .Namespace }}
roleRef:
  kind: ClusterRole
  name: vmagent-node-reader-{{ .HashSuffix }}
  apiGroup: rbac.authorization.k8s.io
{{ end }}
`

const vmagentDeploymentManifestTemplate = `{{ if .EnableVMAgent }}
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
      cluster: {{ .ClusterName | quote }}
      workspace: {{ .Workspace | quote }}
  template:
    metadata:
      labels:
        app: vmagent
        cluster: {{ .ClusterName | quote }}
        workspace: {{ .Workspace | quote }}
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
        image: {{ .VMAgentImage }}
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
{{ end }}
`
