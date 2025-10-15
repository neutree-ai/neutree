package router

const routerServiceAccountTemplate = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: "router-service-account"
  namespace: {{ .Namespace }}
  labels:
    app: router
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
`

const routerRoleTemplate = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: "router-pod-reader"
  namespace: {{ .Namespace }}
  labels:
    app: router
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
rules:
- apiGroups: [""] # "" indicates the core API group
  resources: ["pods"]
  verbs: ["get", "watch", "list", "patch"]
`

const routerRoleBindingTemplate = `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: router-rolebinding
  namespace: {{ .Namespace }}
subjects:
  - kind: ServiceAccount
    name: router-service-account
    namespace: {{ .Namespace }}
roleRef:
  kind: Role
  name: router-pod-reader
  apiGroup: rbac.authorization.k8s.io
`

const routerDeploymentTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: router
  namespace: {{ .Namespace }}
  labels:
    app: router
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
spec:
  replicas: {{ .Replicas }}
  selector:
    matchLabels:
      app: router
      cluster: {{ .ClusterName }}
      workspace: {{ .Workspace }}
  template:
    metadata:
      labels:
        app: router
        cluster: {{ .ClusterName }}
        workspace: {{ .Workspace }}
    spec:
      imagePullSecrets:
      - name: {{ .ImagePullSecret }}
      serviceAccountName: router-service-account
      containers:
      - name: router
        image: {{ .ImagePrefix }}/router:{{ .Version }}
        env:
        - name: LMCACHE_LOG_LEVEL
          value: DEBUG
        args:
        - --host
        - "0.0.0.0"
        - --port
        - "8080"
        - --k8s-namespace
        - {{ .Namespace }}
        - --k8s-label-selector
        - "cluster={{ .ClusterName }},workspace={{ .Workspace }},app=inference"
        - --routing-logic
        - roundrobin
        - --lmcache-controller-port
        - "50051"
        - --engine-stats-interval
        - "30"
        - --session-key
        - "session_id"
        - --service-discovery
        - k8s
        {{- if .Resources }}
        resources:
          limits:
            {{- range $key, $value := .Resources }}
            {{ $key }}: {{ $value }}
            {{- end }}
          requests:
            {{- range $key, $value := .Resources }}
            {{ $key }}: {{ $value }}
            {{- end }}
        {{- end }}
        ports:
        - name: router-port
          containerPort: 8080
        - name: lmcache-port
          containerPort: 50051
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
            scheme: HTTP
          initialDelaySeconds: 10
          periodSeconds: 10
          timeoutSeconds: 5
          successThreshold: 1
          failureThreshold: 3
`

const routerServiceTemplate = `apiVersion: v1
kind: Service
metadata:
  name: router-service
  namespace: {{ .Namespace }}
  labels:
    app: router
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
spec:
  selector:
    app: router
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
  ports:
  - name: http
    port: 80
    targetPort: 8080
  - name: lmcache
    port: 50051
    targetPort: 50051
  type: LoadBalancer
`
