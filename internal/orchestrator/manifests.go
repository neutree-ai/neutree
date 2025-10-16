package orchestrator

var demoDeploymentTemplate = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .EndpointName }}
  namespace: {{ .Namespace }}
  labels:
    engine: {{ .EngineName }}
    engine_version: {{ .EngineVersion }}
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
    routing_logic: {{ .RoutingLogic }}
    app: inference
spec:
  replicas: {{ .Replicas }}
  progressDeadlineSeconds: 1200
  strategy:
    type: Recreate
  selector:
    matchLabels:
      engine: {{ .EngineName }}
      engine_version: {{ .EngineVersion }}
      cluster: {{ .ClusterName }}
      workspace: {{ .Workspace }}
      routing_logic: {{ .RoutingLogic }}
      app: inference
  template:
    metadata:
      labels:
        engine: {{ .EngineName }}
        engine_version: {{ .EngineVersion }}
        cluster: {{ .ClusterName }}
        workspace: {{ .Workspace }}
        routing_logic: {{ .RoutingLogic }}
        app: inference
    spec:
      {{- if .NodeSelector }}
      nodeSelector:
        {{- range $key, $value := .NodeSelector }}
        {{ $key }}: {{ $value }}
        {{- end }}
      {{- end }}
      {{- if .ImagePullSecret }}
      imagePullSecrets:
        - name: {{ .ImagePullSecret }}
      {{- end }}

      {{- if .Volumes }}
      volumes:
{{ .Volumes | toYaml | indent 6 }}
      {{- end }}

      containers:
        - name: {{ .EngineName }}
          image: {{ .ImagePrefix }}/{{ .EngineName }}:{{ .EngineVersion }}
          command:
          - vllm
          - serve
          - {{ .ModelArgs.name }}
          - --host
          - "0.0.0.0"
          - "--port"
          - "8000"
          - --served-model-name
          - {{ .ModelArgs.name }}
          - --task
          - {{ .ModelArgs.task }}
          {{- if .EngineArgs }}
          {{- range $key, $value := .EngineArgs }}
          - --{{ $key }}
		  {{- if ne (printf "%v" $value) "true"}}
          - "{{ $value }}"
		  {{- end }}
          {{- end }}
          {{- end }}
          resources:
            limits:
              {{- range $key, $value := .Resources }}
              {{ $key }}: {{ $value }}
              {{- end }}
            requests:
              {{- range $key, $value := .Resources }}
              {{ $key }}: {{ $value }}
              {{- end }}
          env:
           - name: VLLM_USE_V1
             value: "1"
           {{ range $key, $value := .Env }}
           - name: {{ $key }}
             value: "{{ $value }}"
           {{ end }}
          ports:
            - containerPort: 8000
          startupProbe:
            httpGet:
              path: /health
              port: 8000
            initialDelaySeconds: 5
            timeoutSeconds: 5
            periodSeconds: 10
            successThreshold: 1
            failureThreshold: 120
          {{- if .VolumeMounts }}
          volumeMounts:
{{ .VolumeMounts | toYaml | indent 10 }}
          {{- end }}
`

var deploymentTemplate = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .EndpointName }}
  namespace: {{ .Namespace }}
  labels:
    engine: {{ .EngineName }}
    engine_version: {{ .EngineVersion }}
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
    routing_logic: {{ .RoutingLogic }}
    app: inference
spec:
  replicas: {{ .Replicas }}
  progressDeadlineSeconds: 1200
  selector:
	matchLabels:
	  engine: {{ .EngineName }}
	  engine_version: {{ .EngineVersion }}
	  cluster: {{ .ClusterName }}
	  workspace: {{ .Workspace }}
	  routing_logic: {{ .RoutingLogic }}
	  app: inference
  template:
	metadata:
	  labels:
	    engine: {{ .EngineName }}
	    engine_version: {{ .EngineVersion }}
	    cluster: {{ .ClusterName }}
	    workspace: {{ .Workspace }}
	    routing_logic: {{ .RoutingLogic }}
	    app: inference
	spec:
      {{- if .NodeSelector }}
      nodeSelector:
        {{- range $key, $value := .NodeSelector }}
        {{ $key }}: {{ $value }}
        {{- end }}
      {{- end }}
      {{- if .ImagePullSecret }}
      imagePullSecrets:
        - name: {{ .ImagePullSecret }}
      {{- end }}

      {{- if .Volumes }}
      volumes:
{{ .Volumes | toYaml | indent 6 }}
      {{- end }}

	  containers:
		- name: {{ .EngineName }}
		  image: {{ .ImagePrefix }}/{{ .EngineName }}:{{ .EngineVersion }}
		  command:
		  - neutree
		  - serve
		  - --host
		  - "0.0.0.0"
		  - "--port"
		  - "8080"
		  - --model
		  - {{ .ModelArgs }}
		  - --engine-args
		  - {{ .EngineArgs }}
		  resources:
			limits:
			  {{- range $key, $value := .Resources }}
			  {{ $key }}: {{ $value }}
			  {{- end }}
			requests:
			  {{- range $key, $value := .Resources }}
			  {{ $key }}: {{ $value }}
			  {{- end }}
		  env:
		   - name: VLLM_USE_V1
		     value: "1"
		   {{ range $key, $value := .Env }}
		   - name: {{ $key }}
		     value: "{{ $value }}"
		   {{ end }}
		  ports:
			- containerPort: 8080
		  startupProbe:
			httpGet:
			  path: /health
			  port: 8080
			initialDelaySeconds: 5
			timeoutSeconds: 5
			periodSeconds: 10
			successThreshold: 1
			failureThreshold: 3
          {{- if .VolumeMounts }}
          volumeMounts:
{{ .VolumeMounts | toYaml | indent 10 }}
          {{- end }}
`
