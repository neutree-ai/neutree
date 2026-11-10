package manifests

const AcceleratorExporterDeployment = `{{ range .AcceleratorExporters }}
{{ if .ConfigFileData }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .ConfigMapName }}
  namespace: {{ $.Namespace }}
  labels:
    app: {{ .AppLabel }}
    cluster: {{ $.ClusterName | quote }}
    workspace: {{ $.Workspace | quote }}
    neutree.ai/cluster-version: {{ $.ClusterVersion | quote }}
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
    neutree.ai/accelerator-type: {{ .AcceleratorType | quote }}
    neutree.ai/cluster-version: {{ $.ClusterVersion | quote }}
spec:
  selector:
    matchLabels:
      app: {{ .AppLabel }}
      cluster: {{ $.ClusterName | quote }}
      workspace: {{ $.Workspace | quote }}
  template:
    metadata:
      labels:
        app: {{ .AppLabel }}
        neutree.ai/metrics-target: accelerator-exporter
        neutree.ai/accelerator-type: {{ .AcceleratorType | quote }}
        cluster: {{ $.ClusterName | quote }}
        workspace: {{ $.Workspace | quote }}
        neutree.ai/cluster-version: {{ $.ClusterVersion | quote }}
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
{{ if .Command }}
        command:
{{ .Command | toYaml | indent 8 }}
{{ end }}
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
{{ if .SecurityContext }}
        securityContext:
{{ .SecurityContext | toYaml | indent 10 }}
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
