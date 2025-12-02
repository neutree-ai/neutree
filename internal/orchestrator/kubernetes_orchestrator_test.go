package orchestrator

import (
	"path/filepath"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

var testVllmDeploymentTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .EndpointName }}
  namespace: {{ .Namespace }}
  labels:
    engine: {{ .EngineName }}
    engine_version: {{ .EngineVersion }}
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
    endpoint: {{ .EndpointName }}
    routing_logic: {{ .RoutingLogic }}
    app: inference
spec:
  replicas: {{ .Replicas }}
  progressDeadlineSeconds: 1200
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1
      maxSurge: 0
  selector:
    matchLabels:
      cluster: {{ .ClusterName }}
      workspace: {{ .Workspace }}
      endpoint: {{ .EndpointName }}
      app: inference
  template:
    metadata:
      labels:
        engine: {{ .EngineName }}
        engine_version: {{ .EngineVersion }}
        cluster: {{ .ClusterName }}
        workspace: {{ .Workspace }}
        endpoint: {{ .EndpointName }}
        routing_logic: {{ .RoutingLogic }}
        app: inference
    spec:
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector:
                  matchExpressions:
                    - key: endpoint
                      operator: In
                      values:
                        - {{ .EndpointName }}
                topologyKey: "kubernetes.io/hostname"
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
      initContainers:
        - name: model-downloader
          image: {{ .ImagePrefix }}/neutree/runtime:{{ .NeutreeVersion }}
          command:
            - bash
            - -c
          args:
            - >-
              python3 -m neutree.downloader
              --name="{{ .ModelArgs.name }}"
              --registry_type="{{ .ModelArgs.registry_type }}"
              --registry_path="{{ .ModelArgs.registry_path }}"
              --version="{{ .ModelArgs.version }}"
              --file="{{ .ModelArgs.file }}"
              --task="{{ .ModelArgs.task }}"
          env:
           {{ range $key, $value := .Env }}
           - name: {{ $key }}
             value: "{{ $value }}"
           {{ end }}
          {{- if .VolumeMounts }}
          volumeMounts:
{{ .VolumeMounts | toYaml | indent 10 }}
          {{- end }}

      containers:
        - name: {{ .EngineName }}
          image: {{ .ImagePrefix }}/{{ .ImageRepo }}:{{ .ImageTag }}
          command:
          - vllm
          - serve
          - {{ .ModelArgs.path }}
          - --host
          - "0.0.0.0"
          - "--port"
          - "8000"
          - --served-model-name
          - {{ .ModelArgs.serve_name }}
          - --task
          {{- if eq .ModelArgs.task "text-embedding" }}
          - embedding
          {{- else if eq .ModelArgs.task "text-generation" }}
          - generate
          {{- else if eq .ModelArgs.task "text-rerank" }}
          - rerank
          {{- else }}
          - {{ .ModelArgs.task }}
          {{- end }}
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
          readinessProbe:
            httpGet:
              path: /health
              port: 8000
            initialDelaySeconds: 5
            timeoutSeconds: 5
            periodSeconds: 10
            successThreshold: 1
            failureThreshold: 3
          {{- if .VolumeMounts }}
          volumeMounts:
{{ .VolumeMounts | toYaml | indent 10 }}
          {{- end }}`

// TestBuildVllmDeployment only tests the building of a VLLM default deployment manifest.
func TestBuildVllmDeployment(t *testing.T) {
	data := DeploymentManifestVariables{
		NeutreeVersion:  "v0.1.0",
		ClusterName:     "test-cluster",
		Workspace:       "test-workspace",
		Namespace:       "default",
		ImagePrefix:     "registry.example.com",
		ImageRepo:       "myrepo",
		ImageTag:        "v1.0.0",
		ImagePullSecret: "my-secret",
		EngineName:      "test-engine",
		EngineVersion:   "v1.0.0",
		EndpointName:    "test-endpoint",
		ModelArgs: map[string]interface{}{
			"name":          "gpt-4",
			"task":          "text-generation",
			"path":          "/mnt/models/gpt-4",
			"registry_type": "bentoml",
			"registry_path": "/mnt/registry/gpt-4-model",
			"serve_name":    "gpt-4-serve",
		},
		EngineArgs: map[string]interface{}{
			"max-concurrency": "10",
			"timeout":         "60s",
			"verbose":         true,
			"enable-logging":  "true",
		},
		Resources: map[string]string{
			"cpu":    "500m",
			"memory": "1Gi",
		},
		RoutingLogic: "roundrobin",
		Replicas:     2,
		Volumes: []corev1.Volume{
			{
				Name: "model-volume",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/mnt/model",
					},
				},
			},
		},

		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "model-volume",
				MountPath: "/mnt/model",
			},
		},
	}

	objs, err := buildDeploymentObjects(testVllmDeploymentTemplate, data)
	if err != nil {
		t.Fatalf("Failed to build deployment: %v", err)
	}

	if objs.Items[0].GetName() != "test-endpoint" {
		t.Errorf("Expected deployment name 'test-endpoint', got '%s'", objs.Items[0].GetName())
	}

	// Additional checks can be added here to validate the structure of the generated object
}

var testLlamacppDeploymentTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .EndpointName }}
  namespace: {{ .Namespace }}
  labels:
    engine: {{ .EngineName }}
    engine_version: {{ .EngineVersion }}
    cluster: {{ .ClusterName }}
    workspace: {{ .Workspace }}
    endpoint: {{ .EndpointName }}
    routing_logic: {{ .RoutingLogic }}
    app: inference
spec:
  replicas: {{ .Replicas }}
  progressDeadlineSeconds: 1200
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1
      maxSurge: 0
  selector:
    matchLabels:
      cluster: {{ .ClusterName }}
      workspace: {{ .Workspace }}
      endpoint: {{ .EndpointName }}
      app: inference
  template:
    metadata:
      labels:
        engine: {{ .EngineName }}
        engine_version: {{ .EngineVersion }}
        cluster: {{ .ClusterName }}
        workspace: {{ .Workspace }}
        endpoint: {{ .EndpointName }}
        routing_logic: {{ .RoutingLogic }}
        app: inference
    spec:
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector:
                  matchExpressions:
                    - key: endpoint
                      operator: In
                      values:
                        - {{ .EndpointName }}
                topologyKey: "kubernetes.io/hostname"
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
      initContainers:
        - name: model-downloader
          image: {{ .ImagePrefix }}/neutree/runtime:{{ .NeutreeVersion }}
          command:
            - bash
            - -c
          args:
            - >-
              python3 -m neutree.downloader
              --name="{{ .ModelArgs.name }}"
              --registry_type="{{ .ModelArgs.registry_type }}"
              --registry_path="{{ .ModelArgs.registry_path }}"
              --version="{{ .ModelArgs.version }}"
              --file="{{ .ModelArgs.file }}"
              --task="{{ .ModelArgs.task }}"
          env:
            {{ range $key, $value := .Env }}
            - name: {{ $key }}
              value: "{{ $value }}"
            {{ end }}
          {{- if .VolumeMounts }}
          volumeMounts:
{{ .VolumeMounts | toYaml | indent 10 }}
          {{- end }}
      containers:
        - name: {{ .EngineName }}
          image: {{ .ImagePrefix }}/{{ .ImageRepo }}:{{ .ImageTag }}
          command:
            - bash
            - -c
          args:
            - >-
              python3 -m llama_cpp.server
              --model $(find {{ .ModelArgs.path }} -name "{{ .ModelArgs.file }}" | head -n 1)
              --host 0.0.0.0 --port 8000 --model_alias {{ .ModelArgs.serve_name }}
              {{- if eq .ModelArgs.task "text-embedding" }} --embedding{{- end }}
              {{- if .EngineArgs }}{{- range $key, $value := .EngineArgs }} --{{ $key }}{{- if ne (printf "%v" $value) "true"}} "{{ $value }}"{{- end }}{{- end }}{{- end }}
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
            {{ range $key, $value := .Env }}
            - name: {{ $key }}
              value: "{{ $value }}"
            {{ end }}
          ports:
            - containerPort: 8000
          startupProbe:
            httpGet:
              path: /v1/models
              port: 8000
            initialDelaySeconds: 5
            timeoutSeconds: 5
            periodSeconds: 10
            successThreshold: 1
            failureThreshold: 120
          readinessProbe:
            httpGet:
              path: /v1/models
              port: 8000
            initialDelaySeconds: 5
            timeoutSeconds: 5
            periodSeconds: 10
            successThreshold: 1
            failureThreshold: 3
          {{- if .VolumeMounts }}
          volumeMounts:
{{ .VolumeMounts | toYaml | indent 10 }}
          {{- end }}`

// TestBuildLlamacppDeployment only tests the building of a Llamacpp default deployment manifest.
func TestBuildLlamacppDeployment(t *testing.T) {
	data := DeploymentManifestVariables{
		NeutreeVersion:  "v0.1.0",
		ClusterName:     "test-cluster",
		Workspace:       "test-workspace",
		Namespace:       "default",
		ImagePrefix:     "registry.example.com",
		ImageRepo:       "myrepo",
		ImageTag:        "v1.0.0",
		ImagePullSecret: "my-secret",
		EngineName:      "llama-cpp",
		EngineVersion:   "v0.3.6",
		EndpointName:    "test-endpoint",
		ModelArgs: map[string]interface{}{
			"name":          "qwen2-0.5b-instruct",
			"version":       "v1.0.0",
			"task":          "text-generation",
			"path":          "/mnt/bentoml/models/qwen2-0.5b-instruct-gguf/so42drbrikfceusu",
			"file":          "*q8_0.gguf",
			"serve_name":    "qwen2-0.5b-instruct",
			"registry_type": "bentoml",
			"registry_path": "/mnt/registry/qwen2-0.5b-instruct-model",
		},
		EngineArgs: map[string]interface{}{
			"n_ctx":     "512",
			"n_threads": "1",
		},
		Resources: map[string]string{
			"cpu":    "500m",
			"memory": "1Gi",
		},
		RoutingLogic: "roundrobin",
		Replicas:     2,
		Volumes: []corev1.Volume{
			{
				Name: "model-volume",
				VolumeSource: corev1.VolumeSource{
					NFS: &corev1.NFSVolumeSource{
						Server: "10.255.1.54",
						Path:   "/bentoml",
					},
				},
			},
		},

		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "model-volume",
				MountPath: "/mnt/bentoml",
			},
		},
	}

	objs, err := buildDeploymentObjects(testLlamacppDeploymentTemplate, data)
	if err != nil {
		t.Fatalf("Failed to build deployment: %v", err)
	}

	if objs.Items[0].GetName() != "test-endpoint" {
		t.Errorf("Expected deployment name 'test-endpoint', got '%s'", objs.Items[0].GetName())
	}
}

func TestKubernetesOrchestrator_getImageForAccelerator(t *testing.T) {
	k := &kubernetesOrchestrator{}

	tests := []struct {
		name              string
		engine            *v1.Engine
		version           string
		acceleratorType   string
		expectedImageName string
		expectedImageTag  string
		expectError       bool
		errorContains     string
	}{
		{
			name: "nvidia-gpu image exists",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
							Images: map[string]*v1.EngineImage{
								"nvidia-gpu": {
									ImageName: "vllm-cuda",
									Tag:       "v0.5.0",
								},
								"amd-gpu": {
									ImageName: "vllm-rocm",
									Tag:       "v0.5.0",
								},
							},
						},
					},
				},
			},
			version:           "v0.5.0",
			acceleratorType:   "nvidia-gpu",
			expectedImageName: "vllm-cuda",
			expectedImageTag:  "v0.5.0",
			expectError:       false,
		},
		{
			name: "amd-gpu image with rocm suffix",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
							Images: map[string]*v1.EngineImage{
								"amd-gpu": {
									ImageName: "vllm-rocm",
									Tag:       "v0.5.0",
								},
							},
						},
					},
				},
			},
			version:           "v0.5.0",
			acceleratorType:   "amd-gpu",
			expectedImageName: "vllm-rocm",
			expectedImageTag:  "v0.5.0",
			expectError:       false,
		},
		{
			name: "cpu image",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
							Images: map[string]*v1.EngineImage{
								"cpu": {
									ImageName: "vllm-cpu",
									Tag:       "v0.5.0",
								},
							},
						},
					},
				},
			},
			version:           "v0.5.0",
			acceleratorType:   "cpu",
			expectedImageName: "vllm-cpu",
			expectedImageTag:  "v0.5.0",
			expectError:       false,
		},
		{
			name: "accelerator not supported",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
							Images: map[string]*v1.EngineImage{
								"nvidia-gpu": {
									ImageName: "vllm-cuda",
									Tag:       "v0.5.0",
								},
							},
						},
					},
				},
			},
			version:         "v0.5.0",
			acceleratorType: "intel-gpu",
			expectError:     true,
			errorContains:   "no image configured for accelerator type intel-gpu",
		},
		{
			name: "version not found",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
							Images: map[string]*v1.EngineImage{
								"nvidia-gpu": {
									ImageName: "vllm-cuda",
									Tag:       "v0.5.0",
								},
							},
						},
					},
				},
			},
			version:         "v0.6.0",
			acceleratorType: "nvidia-gpu",
			expectError:     true,
			errorContains:   "engine version v0.6.0 not found",
		},
		{
			name: "no images configured - fallback to legacy",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
							// No Images configured
						},
					},
				},
			},
			version:           "v0.5.0",
			acceleratorType:   "nvidia-gpu",
			expectedImageName: "vllm",
			expectedImageTag:  "v0.5.0",
			expectError:       false,
		},
		{
			name: "image without tag - use version as fallback",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
							Images: map[string]*v1.EngineImage{
								"nvidia-gpu": {
									ImageName: "vllm-cuda",
									// No Tag specified
								},
							},
						},
					},
				},
			},
			version:           "v0.5.0",
			acceleratorType:   "nvidia-gpu",
			expectedImageName: "vllm-cuda",
			expectedImageTag:  "v0.5.0",
			expectError:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageName, imageTag, err := k.getImageForAccelerator(
				tt.engine,
				tt.version,
				tt.acceleratorType,
			)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedImageName, imageName)
				assert.Equal(t, tt.expectedImageTag, imageTag)
			}
		})
	}
}

func TestKubernetesOrchestrator_getImageForAccelerator_MultipleAccelerators(t *testing.T) {
	k := &kubernetesOrchestrator{}

	engine := &v1.Engine{
		Metadata: &v1.Metadata{Name: "vllm"},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{
				{
					Version: "v0.5.0",
					Images: map[string]*v1.EngineImage{
						"nvidia-gpu": {
							ImageName: "vllm-cuda",
							Tag:       "v0.5.0",
						},
						"amd-gpu": {
							ImageName: "vllm-rocm",
							Tag:       "v0.5.0",
						},
						"cpu": {
							ImageName: "vllm-cpu",
							Tag:       "v0.5.0",
						},
					},
				},
			},
		},
	}

	testCases := []struct {
		acceleratorType   string
		expectedImageName string
	}{
		{"nvidia-gpu", "vllm-cuda"},
		{"amd-gpu", "vllm-rocm"},
		{"cpu", "vllm-cpu"},
	}

	for _, tc := range testCases {
		t.Run(tc.acceleratorType, func(t *testing.T) {
			imageName, imageTag, err := k.getImageForAccelerator(
				engine,
				"v0.5.0",
				tc.acceleratorType,
			)

			require.NoError(t, err)
			assert.Equal(t, tc.expectedImageName, imageName)
			assert.Equal(t, "v0.5.0", imageTag)
		})
	}
}

var testBase64DeploymentTemplate = `YXBpVmVyc2lvbjogYXBwcy92MQpraW5kOiBEZXBsb3ltZW50Cm1ldGFkYXRhOgogIG5hbWU6IHt7IC5FbmRwb2ludE5hbWUgfX0KICBuYW1lc3BhY2U6IHt7IC5OYW1lc3BhY2UgfX0KICBsYWJlbHM6CiAgICBlbmdpbmU6IHt7IC5FbmdpbmVOYW1lIH19CiAgICBlbmdpbmVfdmVyc2lvbjoge3sgLkVuZ2luZVZlcnNpb24gfX0KICAgIGNsdXN0ZXI6IHt7IC5DbHVzdGVyTmFtZSB9fQogICAgd29ya3NwYWNlOiB7eyAuV29ya3NwYWNlIH19CiAgICBlbmRwb2ludDoge3sgLkVuZHBvaW50TmFtZSB9fQogICAgcm91dGluZ19sb2dpYzoge3sgLlJvdXRpbmdMb2dpYyB9fQogICAgYXBwOiBpbmZlcmVuY2UKc3BlYzoKICByZXBsaWNhczoge3sgLlJlcGxpY2FzIH19CiAgcHJvZ3Jlc3NEZWFkbGluZVNlY29uZHM6IDEyMDAKICBzdHJhdGVneToKICAgIHR5cGU6IFJvbGxpbmdVcGRhdGUKICAgIHJvbGxpbmdVcGRhdGU6CiAgICAgIG1heFVuYXZhaWxhYmxlOiAxCiAgICAgIG1heFN1cmdlOiAwCiAgc2VsZWN0b3I6CiAgICBtYXRjaExhYmVsczoKICAgICAgY2x1c3Rlcjoge3sgLkNsdXN0ZXJOYW1lIH19CiAgICAgIHdvcmtzcGFjZToge3sgLldvcmtzcGFjZSB9fQogICAgICBlbmRwb2ludDoge3sgLkVuZHBvaW50TmFtZSB9fQogICAgICBhcHA6IGluZmVyZW5jZQogIHRlbXBsYXRlOgogICAgbWV0YWRhdGE6CiAgICAgIGxhYmVsczoKICAgICAgICBlbmdpbmU6IHt7IC5FbmdpbmVOYW1lIH19CiAgICAgICAgZW5naW5lX3ZlcnNpb246IHt7IC5FbmdpbmVWZXJzaW9uIH19CiAgICAgICAgY2x1c3Rlcjoge3sgLkNsdXN0ZXJOYW1lIH19CiAgICAgICAgd29ya3NwYWNlOiB7eyAuV29ya3NwYWNlIH19CiAgICAgICAgZW5kcG9pbnQ6IHt7IC5FbmRwb2ludE5hbWUgfX0KICAgICAgICByb3V0aW5nX2xvZ2ljOiB7eyAuUm91dGluZ0xvZ2ljIH19CiAgICAgICAgYXBwOiBpbmZlcmVuY2UKICAgIHNwZWM6CiAgICAgIGFmZmluaXR5OgogICAgICAgIHBvZEFudGlBZmZpbml0eToKICAgICAgICAgIHByZWZlcnJlZER1cmluZ1NjaGVkdWxpbmdJZ25vcmVkRHVyaW5nRXhlY3V0aW9uOgogICAgICAgICAgICAtIHdlaWdodDogMTAwCiAgICAgICAgICAgICAgcG9kQWZmaW5pdHlUZXJtOgogICAgICAgICAgICAgICAgbGFiZWxTZWxlY3RvcjoKICAgICAgICAgICAgICAgICAgbWF0Y2hFeHByZXNzaW9uczoKICAgICAgICAgICAgICAgICAgICAtIGtleTogZW5kcG9pbnQKICAgICAgICAgICAgICAgICAgICAgIG9wZXJhdG9yOiBJbgogICAgICAgICAgICAgICAgICAgICAgdmFsdWVzOgogICAgICAgICAgICAgICAgICAgICAgICAtIHt7IC5FbmRwb2ludE5hbWUgfX0KICAgICAgICAgICAgICAgIHRvcG9sb2d5S2V5OiAia3ViZXJuZXRlcy5pby9ob3N0bmFtZSIKICAgICAge3stIGlmIC5Ob2RlU2VsZWN0b3IgfX0KICAgICAgbm9kZVNlbGVjdG9yOgogICAgICAgIHt7LSByYW5nZSAka2V5LCAkdmFsdWUgOj0gLk5vZGVTZWxlY3RvciB9fQogICAgICAgIHt7ICRrZXkgfX06IHt7ICR2YWx1ZSB9fQogICAgICAgIHt7LSBlbmQgfX0KICAgICAge3stIGVuZCB9fQogICAgICB7ey0gaWYgLkltYWdlUHVsbFNlY3JldCB9fQogICAgICBpbWFnZVB1bGxTZWNyZXRzOgogICAgICAgIC0gbmFtZToge3sgLkltYWdlUHVsbFNlY3JldCB9fQogICAgICB7ey0gZW5kIH19CgogICAgICB7ey0gaWYgLlZvbHVtZXMgfX0KICAgICAgdm9sdW1lczoKe3sgLlZvbHVtZXMgfCB0b1lhbWwgfCBpbmRlbnQgNiB9fQogICAgICB7ey0gZW5kIH19CiAgICAgIGluaXRDb250YWluZXJzOgogICAgICAgIC0gbmFtZTogbW9kZWwtZG93bmxvYWRlcgogICAgICAgICAgaW1hZ2U6IHt7IC5JbWFnZVByZWZpeCB9fS9uZXV0cmVlL3J1bnRpbWU6e3sgLk5ldXRyZWVWZXJzaW9uIH19CiAgICAgICAgICBjb21tYW5kOgogICAgICAgICAgICAtIGJhc2gKICAgICAgICAgICAgLSAtYwogICAgICAgICAgYXJnczoKICAgICAgICAgICAgLSA+LQogICAgICAgICAgICAgIHB5dGhvbjMgLW0gbmV1dHJlZS5kb3dubG9hZGVyCiAgICAgICAgICAgICAgLS1uYW1lPSJ7eyAuTW9kZWxBcmdzLm5hbWUgfX0iCiAgICAgICAgICAgICAgLS1yZWdpc3RyeV90eXBlPSJ7eyAuTW9kZWxBcmdzLnJlZ2lzdHJ5X3R5cGUgfX0iCiAgICAgICAgICAgICAgLS1yZWdpc3RyeV9wYXRoPSJ7eyAuTW9kZWxBcmdzLnJlZ2lzdHJ5X3BhdGggfX0iCiAgICAgICAgICAgICAgLS12ZXJzaW9uPSJ7eyAuTW9kZWxBcmdzLnZlcnNpb24gfX0iCiAgICAgICAgICAgICAgLS1maWxlPSJ7eyAuTW9kZWxBcmdzLmZpbGUgfX0iCiAgICAgICAgICAgICAgLS10YXNrPSJ7eyAuTW9kZWxBcmdzLnRhc2sgfX0iCiAgICAgICAgICBlbnY6CiAgICAgICAgICAge3sgcmFuZ2UgJGtleSwgJHZhbHVlIDo9IC5FbnYgfX0KICAgICAgICAgICAtIG5hbWU6IHt7ICRrZXkgfX0KICAgICAgICAgICAgIHZhbHVlOiAie3sgJHZhbHVlIH19IgogICAgICAgICAgIHt7IGVuZCB9fQogICAgICAgICAge3stIGlmIC5Wb2x1bWVNb3VudHMgfX0KICAgICAgICAgIHZvbHVtZU1vdW50czoKe3sgLlZvbHVtZU1vdW50cyB8IHRvWWFtbCB8IGluZGVudCAxMCB9fQogICAgICAgICAge3stIGVuZCB9fQoKICAgICAgY29udGFpbmVyczoKICAgICAgICAtIG5hbWU6IHt7IC5FbmdpbmVOYW1lIH19CiAgICAgICAgICBpbWFnZToge3sgLkltYWdlUHJlZml4IH19L3t7IC5JbWFnZVJlcG8gfX06e3sgLkltYWdlVGFnIH19CiAgICAgICAgICBjb21tYW5kOgogICAgICAgICAgLSB2bGxtCiAgICAgICAgICAtIHNlcnZlCiAgICAgICAgICAtIHt7IC5Nb2RlbEFyZ3MucGF0aCB9fQogICAgICAgICAgLSAtLWhvc3QKICAgICAgICAgIC0gIjAuMC4wLjAiCiAgICAgICAgICAtICItLXBvcnQiCiAgICAgICAgICAtICI4MDAwIgogICAgICAgICAgLSAtLXNlcnZlZC1tb2RlbC1uYW1lCiAgICAgICAgICAtIHt7IC5Nb2RlbEFyZ3Muc2VydmVfbmFtZSB9fQogICAgICAgICAgLSAtLXRhc2sKICAgICAgICAgIHt7LSBpZiBlcSAuTW9kZWxBcmdzLnRhc2sgInRleHQtZW1iZWRkaW5nIiB9fQogICAgICAgICAgLSBlbWJlZGRpbmcKICAgICAgICAgIHt7LSBlbHNlIGlmIGVxIC5Nb2RlbEFyZ3MudGFzayAidGV4dC1nZW5lcmF0aW9uIiB9fQogICAgICAgICAgLSBnZW5lcmF0ZQogICAgICAgICAge3stIGVsc2UgaWYgZXEgLk1vZGVsQXJncy50YXNrICJ0ZXh0LXJlcmFuayIgfX0KICAgICAgICAgIC0gcmVyYW5rCiAgICAgICAgICB7ey0gZWxzZSB9fQogICAgICAgICAgLSB7eyAuTW9kZWxBcmdzLnRhc2sgfX0KICAgICAgICAgIHt7LSBlbmQgfX0KICAgICAgICAgIHt7LSBpZiAuRW5naW5lQXJncyB9fQogICAgICAgICAge3stIHJhbmdlICRrZXksICR2YWx1ZSA6PSAuRW5naW5lQXJncyB9fQogICAgICAgICAgLSAtLXt7ICRrZXkgfX0KICAgICAge3stIGlmIG5lIChwcmludGYgIiV2IiAkdmFsdWUpICJ0cnVlIn19CiAgICAgICAgICAtICJ7eyAkdmFsdWUgfX0iCiAgICAgIHt7LSBlbmQgfX0KICAgICAgICAgIHt7LSBlbmQgfX0KICAgICAgICAgIHt7LSBlbmQgfX0KICAgICAgICAgIHJlc291cmNlczoKICAgICAgICAgICAgbGltaXRzOgogICAgICAgICAgICAgIHt7LSByYW5nZSAka2V5LCAkdmFsdWUgOj0gLlJlc291cmNlcyB9fQogICAgICAgICAgICAgIHt7ICRrZXkgfX06IHt7ICR2YWx1ZSB9fQogICAgICAgICAgICAgIHt7LSBlbmQgfX0KICAgICAgICAgICAgcmVxdWVzdHM6CiAgICAgICAgICAgICAge3stIHJhbmdlICRrZXksICR2YWx1ZSA6PSAuUmVzb3VyY2VzIH19CiAgICAgICAgICAgICAge3sgJGtleSB9fToge3sgJHZhbHVlIH19CiAgICAgICAgICAgICAge3stIGVuZCB9fQogICAgICAgICAgZW52OgogICAgICAgICAgIHt7IHJhbmdlICRrZXksICR2YWx1ZSA6PSAuRW52IH19CiAgICAgICAgICAgLSBuYW1lOiB7eyAka2V5IH19CiAgICAgICAgICAgICB2YWx1ZTogInt7ICR2YWx1ZSB9fSIKICAgICAgICAgICB7eyBlbmQgfX0KICAgICAgICAgIHBvcnRzOgogICAgICAgICAgICAtIGNvbnRhaW5lclBvcnQ6IDgwMDAKICAgICAgICAgIHN0YXJ0dXBQcm9iZToKICAgICAgICAgICAgaHR0cEdldDoKICAgICAgICAgICAgICBwYXRoOiAvaGVhbHRoCiAgICAgICAgICAgICAgcG9ydDogODAwMAogICAgICAgICAgICBpbml0aWFsRGVsYXlTZWNvbmRzOiA1CiAgICAgICAgICAgIHRpbWVvdXRTZWNvbmRzOiA1CiAgICAgICAgICAgIHBlcmlvZFNlY29uZHM6IDEwCiAgICAgICAgICAgIHN1Y2Nlc3NUaHJlc2hvbGQ6IDEKICAgICAgICAgICAgZmFpbHVyZVRocmVzaG9sZDogMTIwCiAgICAgICAgICByZWFkaW5lc3NQcm9iZToKICAgICAgICAgICAgaHR0cEdldDoKICAgICAgICAgICAgICBwYXRoOiAvaGVhbHRoCiAgICAgICAgICAgICAgcG9ydDogODAwMAogICAgICAgICAgICBpbml0aWFsRGVsYXlTZWNvbmRzOiA1CiAgICAgICAgICAgIHRpbWVvdXRTZWNvbmRzOiA1CiAgICAgICAgICAgIHBlcmlvZFNlY29uZHM6IDEwCiAgICAgICAgICAgIHN1Y2Nlc3NUaHJlc2hvbGQ6IDEKICAgICAgICAgICAgZmFpbHVyZVRocmVzaG9sZDogMwogICAgICAgICAge3stIGlmIC5Wb2x1bWVNb3VudHMgfX0KICAgICAgICAgIHZvbHVtZU1vdW50czoKe3sgLlZvbHVtZU1vdW50cyB8IHRvWWFtbCB8IGluZGVudCAxMCB9fQogICAgICAgICAge3stIGVuZCB9fQ==`

func Test_getDeployTemplate(t *testing.T) {
	k := &kubernetesOrchestrator{}

	tests := []struct {
		name             string
		endpoint         *v1.Endpoint
		engine           *v1.Engine
		expectedTemplate string
		wantErr          bool
	}{
		{
			name: "success get default template",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "test-endpoint"},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{
						Engine:  "vllm",
						Version: "v0.5.0",
					},
				},
			},
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
							DeployTemplate: map[string]map[string]string{
								"kubernetes": {
									"default": testBase64DeploymentTemplate,
								},
							},
						},
					},
				},
			},
			expectedTemplate: testVllmDeploymentTemplate,
		},
		{
			name: "template not found for orchestrator",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "test-endpoint"},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{
						Engine:  "vllm",
						Version: "v0.5.0",
					},
				},
			},
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "engine version not found",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "test-endpoint"},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{
						Engine:  "vllm",
						Version: "v0.6.0",
					},
				},
			},
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template, err := k.getDeployTemplate(tt.endpoint, tt.engine)
			if (err != nil) != tt.wantErr {
				t.Errorf("getDeployTemplate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if template != tt.expectedTemplate {
				t.Errorf("getDeployTemplate() got = %v, want %v", template, tt.expectedTemplate)
			}
		})
	}

}

func TestKubernetesOrchestrator_setBasicVariables(t *testing.T) {
	k := &kubernetesOrchestrator{}

	numReplicas := 3
	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Name:      "test-endpoint",
			Workspace: "test-workspace",
		},
		Spec: &v1.EndpointSpec{
			Engine: &v1.EndpointEngineSpec{
				Engine:  "vllm",
				Version: "v0.5.0",
			},
			Replicas: v1.ReplicaSpec{
				Num: &numReplicas,
			},
		},
	}

	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Name:      "test-cluster",
			Workspace: "test-workspace",
		},
		Spec: &v1.ClusterSpec{
			Version: "v0.1.0",
		},
	}

	engine := &v1.Engine{
		Metadata: &v1.Metadata{
			Name: "vllm",
		},
	}

	data := &DeploymentManifestVariables{}
	k.setBasicVariables(data, endpoint, cluster, engine)

	assert.Equal(t, "test-endpoint", data.EndpointName)
	assert.Equal(t, "test-cluster", data.ClusterName)
	assert.Equal(t, "test-workspace", data.Workspace)
	assert.Equal(t, "vllm", data.EngineName)
	assert.Equal(t, "v0.5.0", data.EngineVersion)
	assert.Equal(t, int32(3), data.Replicas)
	assert.Equal(t, "roundrobin", data.RoutingLogic)
	assert.NotEmpty(t, data.Namespace)
	assert.NotEmpty(t, data.ImagePullSecret)
	assert.Equal(t, data.NeutreeVersion, "v0.1.0")
}

func TestKubernetesOrchestrator_setRoutingLogic(t *testing.T) {
	k := &kubernetesOrchestrator{}

	tests := []struct {
		name          string
		endpoint      *v1.Endpoint
		expectedLogic string
		initialLogic  string
	}{
		{
			name: "with custom routing logic",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					DeploymentOptions: map[string]interface{}{
						"scheduler": map[string]interface{}{
							"type": "leastconn",
						},
					},
				},
			},
			initialLogic:  "roundrobin",
			expectedLogic: "leastconn",
		},
		{
			name: "without scheduler config",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					DeploymentOptions: map[string]interface{}{},
				},
			},
			initialLogic:  "roundrobin",
			expectedLogic: "roundrobin",
		},
		{
			name: "with nil deployment options",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{},
			},
			initialLogic:  "roundrobin",
			expectedLogic: "roundrobin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := &DeploymentManifestVariables{
				RoutingLogic: tt.initialLogic,
			}
			k.setRoutingLogic(data, tt.endpoint)
			assert.Equal(t, tt.expectedLogic, data.RoutingLogic)
		})
	}
}

func TestKubernetesOrchestrator_setEngineArgs(t *testing.T) {
	k := &kubernetesOrchestrator{}

	tests := []struct {
		name         string
		endpoint     *v1.Endpoint
		expectedArgs map[string]interface{}
	}{
		{
			name: "with engine args",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Variables: map[string]interface{}{
						"engine_args": map[string]interface{}{
							"max-model-len":        "4096",
							"tensor-parallel-size": "2",
						},
					},
				},
			},
			expectedArgs: map[string]interface{}{
				"max-model-len":        "4096",
				"tensor-parallel-size": "2",
			},
		},
		{
			name: "without engine args",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Variables: map[string]interface{}{
						"other_var": "value",
					},
				},
			},
			expectedArgs: nil,
		},
		{
			name: "with nil variables",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{},
			},
			expectedArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := &DeploymentManifestVariables{}
			k.setEngineArgs(data, tt.endpoint)
			assert.Equal(t, tt.expectedArgs, data.EngineArgs)
		})
	}
}

func TestKubernetesOrchestrator_setEnvironmentVariables(t *testing.T) {
	k := &kubernetesOrchestrator{}

	tests := []struct {
		name        string
		endpoint    *v1.Endpoint
		expectedEnv map[string]string
	}{
		{
			name: "with environment variables",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Env: map[string]string{
						"CUDA_VISIBLE_DEVICES": "0,1",
						"NCCL_DEBUG":           "INFO",
					},
				},
			},
			expectedEnv: map[string]string{
				"CUDA_VISIBLE_DEVICES": "0,1",
				"NCCL_DEBUG":           "INFO",
			},
		},
		{
			name: "without environment variables",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{},
			},
			expectedEnv: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := &DeploymentManifestVariables{}
			k.setEnvironmentVariables(data, tt.endpoint)
			assert.Equal(t, tt.expectedEnv, data.Env)
		})
	}
}

func TestKubernetesOrchestrator_setModelArgs(t *testing.T) {
	k := &kubernetesOrchestrator{}

	tests := []struct {
		name              string
		endpoint          *v1.Endpoint
		modelRegistry     *v1.ModelRegistry
		expectedModelArgs map[string]interface{}
	}{
		{
			name: "complete model spec",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name:     "llama-2-7b",
						Version:  "v1.0",
						File:     "model.safetensors",
						Task:     "text-generation",
						Registry: "huggingface",
					},
				},
			},
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "hf-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "https://huggingface.co/",
				},
			},
			expectedModelArgs: map[string]interface{}{
				"name":          "llama-2-7b",
				"version":       "v1.0",
				"file":          "model.safetensors",
				"task":          "text-generation",
				"path":          "llama-2-7b",
				"registry_type": string(v1.HuggingFaceModelRegistryType),
				"serve_name":    "llama-2-7b",
			},
		},
		{
			name: "minimal model spec",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name: "gpt-model",
						Task: "text-generation",
					},
				},
			},
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "bentoml-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
					Url:  "nfs://10.255.1.54:/mnt/bentoml",
				},
			},
			expectedModelArgs: map[string]interface{}{
				"name":          "gpt-model",
				"version":       "",
				"file":          "",
				"task":          "text-generation",
				"path":          "gpt-model",
				"registry_type": string(v1.BentoMLModelRegistryType),
				"serve_name":    "gpt-model",
			},
		},
		{
			name: "serve_name with version when registry is BentoML",
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name:     "gpt-model",
						Version:  "v2.0",
						Task:     "text-generation",
						Registry: "bentoml",
					},
				},
			},
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "bentoml-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
					Url:  "nfs://192.168.1.100/bentoml",
				},
			},
			expectedModelArgs: map[string]interface{}{
				"name":          "gpt-model",
				"version":       "v2.0",
				"file":          "",
				"task":          "text-generation",
				"path":          "gpt-model",
				"registry_type": string(v1.BentoMLModelRegistryType),
				"serve_name":    "gpt-model:v2.0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := &DeploymentManifestVariables{}
			k.setModelArgs(data, tt.endpoint, tt.modelRegistry)
			assert.Equal(t, tt.expectedModelArgs, data.ModelArgs)
		})
	}
}

func TestKubernetesOrchestrator_addSharedMemoryVolume(t *testing.T) {
	k := &kubernetesOrchestrator{}

	data := &DeploymentManifestVariables{}
	k.addSharedMemoryVolume(data)

	require.Len(t, data.Volumes, 1)
	assert.Equal(t, "dshm", data.Volumes[0].Name)
	assert.NotNil(t, data.Volumes[0].VolumeSource.EmptyDir)
	assert.Equal(t, corev1.StorageMediumMemory, data.Volumes[0].VolumeSource.EmptyDir.Medium)

	require.Len(t, data.VolumeMounts, 1)
	assert.Equal(t, "dshm", data.VolumeMounts[0].Name)
	assert.Equal(t, "/dev/shm", data.VolumeMounts[0].MountPath)
}

func TestKubernetesOrchestrator_setModelRegistryVariables_HuggingFace(t *testing.T) {
	k := &kubernetesOrchestrator{}
	modelPathPrefix := filepath.Join(v1.DefaultK8sClusterModelCacheMountPath, v1.HuggingFaceModelRegistryType)
	tests := []struct {
		name          string
		modelRegistry *v1.ModelRegistry
		endpoint      *v1.Endpoint
		expected      *DeploymentManifestVariables
		wantErr       bool
	}{
		{
			name: "HuggingFace with credentials",
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "hf-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type:        v1.HuggingFaceModelRegistryType,
					Url:         "https://huggingface.co/",
					Credentials: "hf_test_token",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name: "test-model",
					},
				},
			},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"registry_path": "test-model",
					"path":          filepath.Join(modelPathPrefix, "test-model"),
				},
				Env: map[string]string{
					v1.HFEndpoint: "https://huggingface.co",
					v1.HFTokenEnv: "hf_test_token",
				},
			},
		},
		{
			name: "HuggingFace without credentials",
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "hf-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "https://huggingface.co/",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name: "test-model",
					},
				},
			},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"registry_path": "test-model",
					"path":          filepath.Join(modelPathPrefix, "test-model"),
				},
				Env: map[string]string{
					v1.HFEndpoint: "https://huggingface.co",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := &DeploymentManifestVariables{
				Env:       map[string]string{},
				ModelArgs: map[string]interface{}{},
			}
			err := k.setModelRegistryVariables(data, tt.endpoint, tt.modelRegistry)
			if tt.wantErr {
				assert.Error(t, err)
				return
			} else {
				assert.NoError(t, err)
				eq, _, err := util.JsonEqual(data, tt.expected)
				require.NoError(t, err)
				if !eq {
					t.Errorf("expected and actual DeploymentManifestVariables do not match, want: %+v, got: %+v", tt.expected, data)
				}
			}
		})
	}
}

func TestKubernetesOrchestrator_setModelRegistryVariables_BentoML(t *testing.T) {
	k := &kubernetesOrchestrator{}

	tests := []struct {
		name          string
		modelRegistry *v1.ModelRegistry
		endpoint      *v1.Endpoint
		expected      *DeploymentManifestVariables
		expectError   bool
	}{
		{
			name: "BentoML with NFS - specific version",
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "bentoml-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
					Url:  "nfs://192.168.1.100/bentoml",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name:    "llama-2-7b",
						Version: "v1.0",
						File:    "model.safetensors",
					},
				},
			},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"path":          "/models-cache/bentoml/llama-2-7b/v1.0",
					"registry_path": "/mnt/bentoml/models/llama-2-7b/v1.0",
				},
				Volumes: []corev1.Volume{
					{
						Name: "bentoml-model-registry",
						VolumeSource: corev1.VolumeSource{
							NFS: &corev1.NFSVolumeSource{
								Server: "192.168.1.100",
								Path:   "/bentoml",
							},
						},
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "bentoml-model-registry",
						MountPath: "/mnt/bentoml",
					},
				},
			},
			expectError: false,
		},
		{
			name: "BentoML with NFS - without file",
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "bentoml-registry",
				},
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
					Url:  "nfs://192.168.1.100/bentoml",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name:    "gpt-model",
						Version: "v2.0",
					},
				},
			},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"path":          "/models-cache/bentoml/gpt-model/v2.0",
					"registry_path": "/mnt/bentoml/models/gpt-model/v2.0",
				},
				Volumes: []corev1.Volume{
					{
						Name: "bentoml-model-registry",
						VolumeSource: corev1.VolumeSource{
							NFS: &corev1.NFSVolumeSource{
								Server: "192.168.1.100",
								Path:   "/bentoml",
							},
						},
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "bentoml-model-registry",
						MountPath: "/mnt/bentoml",
					},
				},
			},
			expectError: false,
		},
		{
			name: "BentoML without NFS scheme",
			modelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{
					Name: "bentoml-local",
				},
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
					Url:  "file:///local/bentoml",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name:    "test-model",
						Version: "v1.0",
					},
				},
			},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{},
			}

			err := k.setModelRegistryVariables(data, tt.endpoint, tt.modelRegistry)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				eq, _, err := util.JsonEqual(data, tt.expected)
				require.NoError(t, err)
				if !eq {
					t.Errorf("expected and actual DeploymentManifestVariables do not match, want: %+v, got: %+v", tt.expected, data)
				}
			}
		})
	}
}

func TestKubernetesOrchestrator_setDeployImageVariables(t *testing.T) {
	tests := []struct {
		name                string
		endpoint            *v1.Endpoint
		engine              *v1.Engine
		imageRegistry       *v1.ImageRegistry
		expectedImagePrefix string
		expectedImage       string
		expectedTag         string
		expectError         bool
	}{
		{
			name: "with new image system - nvidia gpu",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "test-endpoint"},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{
						Engine:  "vllm",
						Version: "v0.5.0",
					},
					Resources: &v1.ResourceSpec{
						Accelerator: map[string]string{
							"type": "nvidia-gpu",
						},
					},
				},
			},
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
							Images: map[string]*v1.EngineImage{
								"nvidia-gpu": {
									ImageName: "vllm-cuda",
									Tag:       "v0.5.0-cuda12.1",
								},
							},
						},
					},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "default"},
				Spec: &v1.ImageRegistrySpec{
					URL:        "https://registry.neutree.ai",
					Repository: "neutree",
				},
			},
			expectedImagePrefix: "registry.neutree.ai/neutree",
			expectedImage:       "vllm-cuda",
			expectedTag:         "v0.5.0-cuda12.1",
			expectError:         false,
		},
		{
			name: "with new image system - cpu",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "test-endpoint"},
				Spec: &v1.EndpointSpec{
					Engine: &v1.EndpointEngineSpec{
						Engine:  "vllm",
						Version: "v0.5.0",
					},
					Resources: &v1.ResourceSpec{
						CPU: floatPtr(4.0),
					},
				},
			},
			engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "vllm"},
				Spec: &v1.EngineSpec{
					Versions: []*v1.EngineVersion{
						{
							Version: "v0.5.0",
							Images: map[string]*v1.EngineImage{
								"cpu": {
									ImageName: "vllm-cpu",
									Tag:       "v0.5.0",
								},
							},
						},
					},
				},
			},
			imageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "default"},
				Spec: &v1.ImageRegistrySpec{
					URL:        "https://registry.neutree.ai",
					Repository: "neutree",
				},
			},
			expectedImagePrefix: "registry.neutree.ai/neutree",
			expectedImage:       "vllm-cpu",
			expectedTag:         "v0.5.0",
			expectError:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := &kubernetesOrchestrator{}
			data := &DeploymentManifestVariables{}

			err := k.setDeployImageVariables(data, tt.endpoint, tt.engine, tt.imageRegistry)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedImage, data.ImageRepo)
				assert.Equal(t, tt.expectedTag, data.ImageTag)
			}
		})
	}
}

func TestKubernetesOrchestrator_setModelCacheVariables(t *testing.T) {
	k := &kubernetesOrchestrator{}

	tests := []struct {
		name             string
		cluster          *v1.Cluster
		expectedVolCount int
		expectedEnvKeys  []string
		expectError      bool
	}{
		{
			name: "cluster with model cache",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name:      "test-cluster",
					Workspace: "test-workspace",
				},
				Spec: &v1.ClusterSpec{
					Type: "kubernetes",
					Config: v1.KubernetesClusterConfig{
						CommonClusterConfig: v1.CommonClusterConfig{
							ModelCaches: []v1.ModelCache{
								{
									ModelRegistryType: v1.HuggingFaceModelRegistryType,
									HostPath: &corev1.HostPathVolumeSource{
										Path: "/data/huggingface",
									},
								},
							},
						},
					},
				},
			},
			expectedVolCount: 2,
			expectedEnvKeys:  []string{v1.ModelCacheDirENV},
			expectError:      false,
		},
		{
			name: "cluster without model cache",
			cluster: &v1.Cluster{
				Metadata: &v1.Metadata{
					Name:      "test-cluster",
					Workspace: "test-workspace",
				},
				Spec: &v1.ClusterSpec{
					Type: "kubernetes",
					Config: v1.KubernetesClusterConfig{
						CommonClusterConfig: v1.CommonClusterConfig{
							ModelCaches: []v1.ModelCache{},
						},
					},
				},
			},
			expectedVolCount: 1,
			expectedEnvKeys:  []string{v1.ModelCacheDirENV},
			expectError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := &DeploymentManifestVariables{
				Env: map[string]string{},
			}

			err := k.setModelCacheVariables(data, tt.cluster)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, data.Volumes, tt.expectedVolCount)
				assert.Len(t, data.VolumeMounts, tt.expectedVolCount)

				for _, key := range tt.expectedEnvKeys {
					assert.Contains(t, data.Env, key)
				}
			}
		})
	}
}

func TestGenerateModelCacheConfig(t *testing.T) {
	tests := []struct {
		name            string
		modelCaches     []v1.ModelCache
		expectedEnvs    map[string]string
		expectedVolumes []corev1.Volume
		expectedMounts  []corev1.VolumeMount
	}{
		{
			name: "single model cache",
			modelCaches: []v1.ModelCache{
				{
					ModelRegistryType: v1.HuggingFaceModelRegistryType,
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/data/huggingface",
					},
				},
			},
			expectedEnvs: map[string]string{
				v1.ModelCacheDirENV: "/models-cache",
			},
			expectedVolumes: []corev1.Volume{
				{
					Name: "models-cache-tmp",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium: corev1.StorageMediumDefault,
						},
					},
				},
				{
					Name: "models-cache-hugging-face",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/data/huggingface",
						},
					},
				},
			},
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      "models-cache-tmp",
					MountPath: "/models-cache",
				},
				{
					Name:      "models-cache-hugging-face",
					MountPath: "/models-cache/hugging-face",
				},
			},
		},
		{
			name: "multiple model caches",
			modelCaches: []v1.ModelCache{
				{
					ModelRegistryType: v1.HuggingFaceModelRegistryType,
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/data/huggingface",
					},
				},
				{
					ModelRegistryType: v1.BentoMLModelRegistryType,
					NFS: &corev1.NFSVolumeSource{
						Server: "192.168.1.1",
						Path:   "/models",
					},
				},
			},
			expectedEnvs: map[string]string{
				v1.ModelCacheDirENV: "/models-cache",
			},
			expectedVolumes: []corev1.Volume{
				{
					Name: "models-cache-tmp",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium: corev1.StorageMediumDefault,
						},
					},
				},
				{
					Name: "models-cache-hugging-face",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/data/huggingface",
						},
					},
				},
				{
					Name: "models-cache-bentoml",
					VolumeSource: corev1.VolumeSource{
						NFS: &corev1.NFSVolumeSource{
							Server: "192.168.1.1",
							Path:   "/models",
						},
					},
				},
			},
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      "models-cache-tmp",
					MountPath: "/models-cache",
				},
				{
					Name:      "models-cache-hugging-face",
					MountPath: "/models-cache/hugging-face",
				},
				{
					Name:      "models-cache-bentoml",
					MountPath: "/models-cache/bentoml",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			volumes, mounts, envs := generateModelCacheConfig(tt.modelCaches)

			eq, _, err := util.JsonEqual(tt.expectedVolumes, volumes)
			require.NoError(t, err)
			if !eq {
				t.Errorf("expected and actual volumes do not match, want: %+v, got: %+v", tt.expectedVolumes, volumes)
			}

			eq, _, err = util.JsonEqual(tt.expectedMounts, mounts)
			require.NoError(t, err)
			if !eq {
				t.Errorf("expected and actual mounts do not match, want: %+v, got: %+v", tt.expectedMounts, mounts)
			}

			eq, _, err = util.JsonEqual(tt.expectedEnvs, envs)
			require.NoError(t, err)
			if !eq {
				t.Errorf("expected and actual envs do not match, want: %+v, got: %+v", tt.expectedEnvs, envs)
			}
		})
	}
}

func floatPtr(f float64) *float64 {
	return &f
}
