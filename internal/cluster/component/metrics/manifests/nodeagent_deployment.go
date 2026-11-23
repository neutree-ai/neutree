package manifests

const nodeAgentRBACManifestTemplate = `{{ if .EnableNeutreeNodeAgentMetrics }}
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
    cluster: {{ .ClusterName | quote }}
    workspace: {{ .Workspace | quote }}
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
    cluster: {{ .ClusterName | quote }}
    workspace: {{ .Workspace | quote }}
subjects:
- kind: ServiceAccount
  name: {{ .NeutreeNodeAgentMetricsName }}
  namespace: {{ .Namespace }}
roleRef:
  kind: ClusterRole
  name: {{ .NeutreeNodeAgentMetricsName }}-{{ .HashSuffix }}
  apiGroup: rbac.authorization.k8s.io
{{ end }}
`

const nodeAgentDeploymentManifestTemplate = `{{ if .EnableNeutreeNodeAgentMetrics }}
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: {{ .NeutreeNodeAgentMetricsName }}
  namespace: {{ .Namespace }}
  labels:
    app: {{ .NeutreeNodeAgentMetricsName }}
    neutree.ai/metrics-target: node-agent
{{ if .NodeAgent.Type }}
    neutree.ai/node-agent-type: {{ .NodeAgent.Type }}
{{ end }}
    neutree.ai/cluster-version: {{ .ClusterVersion }}
spec:
  selector:
    matchLabels:
      app: {{ .NeutreeNodeAgentMetricsName }}
      cluster: {{ .ClusterName | quote }}
      workspace: {{ .Workspace | quote }}
  template:
    metadata:
      labels:
        app: {{ .NeutreeNodeAgentMetricsName }}
        neutree.ai/metrics-target: node-agent
{{ if .NodeAgent.Type }}
        neutree.ai/node-agent-type: {{ .NodeAgent.Type }}
{{ end }}
        cluster: {{ .ClusterName | quote }}
        workspace: {{ .Workspace | quote }}
        neutree.ai/cluster-version: {{ .ClusterVersion }}
    spec:
{{ if .NodeAgent.RuntimeClassName }}
      runtimeClassName: {{ .NodeAgent.RuntimeClassName }}
{{ end }}
{{ if .NodeAgent.NodeSelector }}
      nodeSelector:
{{ .NodeAgent.NodeSelector | toYaml | indent 8 }}
{{ end }}
{{ if .NodeAgent.Affinity }}
      affinity:
{{ .NodeAgent.Affinity | toYaml | indent 8 }}
{{ end }}
      serviceAccountName: {{ .NeutreeNodeAgentMetricsName }}
      imagePullSecrets:
      - name: {{ .ImagePullSecret }}
{{ if .NodeAgent.InitContainers }}
      initContainers:
{{ .NodeAgent.InitContainers | toYaml | indent 6 }}
{{ end }}
      containers:
      - name: neutree-node-agent
        image: {{ .NeutreeNodeAgentMetricsImage }}
        args:
        - --listen-address=:{{ .NeutreeNodeAgentMetricsPort }}
{{ if .NodeAgent.UseLegacyContract }}
        - --cluster-type={{ .NodeAgentLegacyClusterType }}
        - --metrics-mode={{ .MetricsMode }}
{{ else }}
        - --cluster-type={{ .NodeAgentClusterType }}
{{ end }}
        - --node=$(NODE_NAME)
        - --node-ip=$(NODE_IP)
{{ if .NodeAgent.AcceleratorType }}
        - --accelerator-type={{ .NodeAgent.AcceleratorType }}
        - --accelerator-exporter-port={{ .NodeAgent.AcceleratorExporterPort }}
        - --accelerator-exporter-metrics-path={{ .NodeAgent.AcceleratorExporterMetricsPath }}
{{ end }}
        env:
{{ if .NodeAgent.Env }}
{{ .NodeAgent.Env | toYaml | indent 8 }}
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
{{ if .NodeAgent.SecurityContext }}
        securityContext:
{{ .NodeAgent.SecurityContext | toYaml | indent 10 }}
{{ end }}
        volumeMounts:
{{ .NodeAgent.VolumeMounts | toYaml | indent 8 }}
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
{{ .NodeAgent.Volumes | toYaml | indent 6 }}
{{ end }}
`

const NodeAgentTemplate = nodeAgentRBACManifestTemplate + nodeAgentDeploymentManifestTemplate
