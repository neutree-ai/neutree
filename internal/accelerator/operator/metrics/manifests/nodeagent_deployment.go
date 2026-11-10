package manifests

const NodeAgentRBAC = `{{ if .EnableNeutreeNodeAgentMetrics }}
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

const NodeAgentDeployment = `{{ if .EnableNeutreeNodeAgentMetrics }}
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: {{ .NeutreeNodeAgentMetricsName }}
  namespace: {{ .Namespace }}
  labels:
    app: {{ .NeutreeNodeAgentMetricsName }}
    neutree.ai/metrics-target: node-agent
    neutree.ai/cluster-version: {{ .ClusterVersion | quote }}
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
        cluster: {{ .ClusterName | quote }}
        workspace: {{ .Workspace | quote }}
        neutree.ai/cluster-version: {{ .ClusterVersion | quote }}
    spec:
{{ if .NodeAgent.NodeSelector }}
      nodeSelector:
{{ .NodeAgent.NodeSelector | toYaml | indent 8 }}
{{ end }}
      serviceAccountName: {{ .NeutreeNodeAgentMetricsName }}
      imagePullSecrets:
      - name: {{ .ImagePullSecret }}
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
