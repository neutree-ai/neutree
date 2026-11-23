package manifests

const nodeExporterDeploymentManifestTemplate = `{{ if .EnableNodeExporter }}
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
      cluster: {{ .ClusterName | quote }}
      workspace: {{ .Workspace | quote }}
  template:
    metadata:
      labels:
        app: {{ .NodeExporterName }}
        cluster: {{ .ClusterName | quote }}
        workspace: {{ .Workspace | quote }}
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
{{ end }}
`
