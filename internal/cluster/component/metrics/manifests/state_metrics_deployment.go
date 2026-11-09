package manifests

const stateMetricsDeploymentManifestTemplate = `{{ if .EnableKubeStateMetrics }}
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
        cluster: {{ .ClusterName | quote }}
        workspace: {{ .Workspace | quote }}
        neutree.ai/cluster-version: {{ .ClusterVersion }}
    spec:
      imagePullSecrets:
      - name: {{ .ImagePullSecret }}
      serviceAccountName: neutree-kube-state-metrics
      containers:
      - name: kube-state-metrics
        image: {{ .KubeStateMetricsImage }}
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
`
