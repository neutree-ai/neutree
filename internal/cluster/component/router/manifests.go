package router

import v1 "github.com/neutree-ai/neutree/api/v1"

var routerMainifestTemplate = `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: "router-service-account"
  namespace: {{ .Namespace }}
  labels:
    app: router
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: "router-pod-reader"
  namespace: {{ .Namespace }}
  labels:
    app: router
rules:
- apiGroups: [""] # "" indicates the core API group
  resources: ["pods"]
  verbs: ["get", "watch", "list", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: router-rolebinding
  namespace: {{ .Namespace }}
  labels:
    app: router
subjects:
  - kind: ServiceAccount
    name: router-service-account
    namespace: {{ .Namespace }}
roleRef:
  kind: Role
  name: router-pod-reader
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: router
  namespace: {{ .Namespace }}
  labels:
    app: router
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
                  - router
              topologyKey: "kubernetes.io/hostname"
      imagePullSecrets:
      - name: {{ .ImagePullSecret }}
      serviceAccountName: router-service-account
      containers:
      - name: router
        image: {{ .ImagePrefix }}/neutree/router:{{ .Version }}
        env:
        - name: LMCACHE_LOG_LEVEL
          value: DEBUG
        args:
        - --host
        - "0.0.0.0"
        - --port
        - "8000"
        - --k8s-namespace
        - {{ .Namespace }}
        - --k8s-label-selector
        - "cluster={{ .ClusterName }},workspace={{ .Workspace }},app=inference"
        - --routing-logic
        - roundrobin
        - --lmcache-controller-port
        - "50051"
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
          containerPort: 8000
        readinessProbe:
          httpGet:
            path: /health
            port: 8000
            scheme: HTTP
          initialDelaySeconds: 10
          periodSeconds: 10
          timeoutSeconds: 5
          successThreshold: 1
          failureThreshold: 3
---
apiVersion: v1
kind: Service
metadata:
  name: router-service
  namespace: {{ .Namespace }}
  labels:
    app: router
spec:
  selector:
    app: router
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
  ports:
  - name: http
    port: 8000
    targetPort: 8000
  type: {{ .AccessMode }}
`

// RouteManifestVariables holds the variables for rendering route manifests
type RouteManifestVariables struct {
	ClusterName     string
	Workspace       string
	Namespace       string
	ImagePrefix     string
	ImagePullSecret string
	Version         string
	Replicas        int
	Resources       map[string]string
	AccessMode      string
}

// buildManifestVariables creates the data structure for rendering manifests
func (r *RouterComponent) buildManifestVariables() RouteManifestVariables {
	// default to cluster version if router version is not specified
	version := r.cluster.Spec.Version
	if r.config.Router.Version != "" {
		version = r.config.Router.Version
	}

	accessMode := v1.KubernetesAccessModeLoadBalancer
	if r.config.Router.AccessMode != "" {
		accessMode = r.config.Router.AccessMode
	}

	replicas := 2
	if r.config.Router.Replicas > 0 {
		replicas = r.config.Router.Replicas
	}

	resources := map[string]string{
		"cpu":    "500m",
		"memory": "512Mi",
	}

	if r.config.Router.Resources != nil {
		if cpu, ok := r.config.Router.Resources["cpu"]; ok {
			resources["cpu"] = cpu
		}

		if memory, ok := r.config.Router.Resources["memory"]; ok {
			resources["memory"] = memory
		}
	}

	return RouteManifestVariables{
		ClusterName:     r.cluster.GetName(),
		Workspace:       r.cluster.GetWorkspace(),
		Namespace:       r.namespace,
		ImagePrefix:     r.imagePrefix,
		ImagePullSecret: r.imagePullSecret,
		Version:         version,
		Replicas:        replicas,
		Resources:       resources,
		AccessMode:      string(accessMode),
	}
}
