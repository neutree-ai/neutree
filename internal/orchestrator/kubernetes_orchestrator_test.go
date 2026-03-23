package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// FakeK8sClient wraps controller-runtime fake client with builder methods
type FakeK8sClient struct {
	client.Client
	t *testing.T
}

func NewFakeK8sClient(t *testing.T) *FakeK8sClient {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	return &FakeK8sClient{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		t:      t,
	}
}

func (f *FakeK8sClient) WithGetError(err error) *FakeK8sClient {
	// For error simulation, we'll use interceptor pattern
	// Return a wrapper that simulates errors
	return &FakeK8sClient{
		Client: &errorClient{Client: f.Client, getError: err},
		t:      f.t,
	}
}

func (f *FakeK8sClient) WithDeploymentNotFound() *FakeK8sClient {
	// No deployment to add, will return NotFound naturally
	return f
}

func (f *FakeK8sClient) WithDeployment(name string, replicas, readyReplicas, updatedReplicas int32) *FakeK8sClient {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "test-namespace",
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":      "inference",
					"endpoint": name,
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           replicas,
			ReadyReplicas:      readyReplicas,
			UpdatedReplicas:    updatedReplicas,
			AvailableReplicas:  readyReplicas,
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentProgressing,
					Status: corev1.ConditionTrue,
					Reason: "NewReplicaSetAvailable",
				},
				{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	err := f.Client.Create(context.Background(), deployment)
	if err != nil {
		f.t.Fatalf("failed to create deployment: %v", err)
	}
	return f
}

func (f *FakeK8sClient) WithDeploymentWithCondition(name string, replicas, readyReplicas, updatedReplicas int32, condType, reason, message string) *FakeK8sClient {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "test-namespace",
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":      "inference",
					"endpoint": name,
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           replicas,
			ReadyReplicas:      readyReplicas,
			UpdatedReplicas:    updatedReplicas,
			AvailableReplicas:  readyReplicas,
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:    appsv1.DeploymentConditionType(condType),
					Status:  corev1.ConditionFalse,
					Reason:  reason,
					Message: message,
				},
			},
		},
	}

	err := f.Client.Create(context.Background(), deployment)
	if err != nil {
		f.t.Fatalf("failed to create deployment: %v", err)
	}
	return f
}

func (f *FakeK8sClient) WithPods(count int) *FakeK8sClient {
	for i := 0; i < count; i++ {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("pod-%d", i),
				Namespace: "test-namespace",
				Labels: map[string]string{
					"app":      "inference",
					"endpoint": "chat-model",
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name:  "main-container",
						Ready: true,
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
		}
		err := f.Client.Create(context.Background(), pod)
		if err != nil {
			f.t.Fatalf("failed to create pod: %v", err)
		}
	}
	return f
}

func (f *FakeK8sClient) WithPodInCrashLoopBackOff(containerName string, restartCount int32) *FakeK8sClient {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-crash",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat-model",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         containerName,
					Ready:        false,
					RestartCount: restartCount,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "CrashLoopBackOff",
							Message: "Container is crashing",
						},
					},
				},
			},
		},
	}
	err := f.Client.Create(context.Background(), pod)
	if err != nil {
		f.t.Fatalf("failed to create pod: %v", err)
	}
	return f
}

func (f *FakeK8sClient) WithPodInImagePullBackOff(containerName string) *FakeK8sClient {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-image-pull",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat-model",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  containerName,
					Ready: false,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ImagePullBackOff",
							Message: "Failed to pull image",
						},
					},
				},
			},
		},
	}
	err := f.Client.Create(context.Background(), pod)
	if err != nil {
		f.t.Fatalf("failed to create pod: %v", err)
	}
	return f
}

func (f *FakeK8sClient) WithPodOOMKilled(containerName string) *FakeK8sClient {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-oom",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat-model",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  containerName,
					Ready: false,
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							Reason:  "OOMKilled",
							Message: "Out of memory",
						},
					},
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: "CrashLoopBackOff",
						},
					},
				},
			},
		},
	}
	err := f.Client.Create(context.Background(), pod)
	if err != nil {
		f.t.Fatalf("failed to create pod: %v", err)
	}
	return f
}

func (f *FakeK8sClient) WithUnschedulablePod(message string) *FakeK8sClient {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-unschedulable",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat-model",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{
				{
					Type:    corev1.PodScheduled,
					Status:  corev1.ConditionFalse,
					Reason:  "Unschedulable",
					Message: message,
				},
			},
		},
	}
	err := f.Client.Create(context.Background(), pod)
	if err != nil {
		f.t.Fatalf("failed to create pod: %v", err)
	}
	return f
}

func (f *FakeK8sClient) AssertExpectations() {
	// No-op for compatibility with test code
}

// errorClient wraps a client to simulate errors
type errorClient struct {
	client.Client
	getError error
}

func (e *errorClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if e.getError != nil {
		return e.getError
	}
	return e.Client.Get(ctx, key, obj, opts...)
}

var testVllmDeploymentTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .EndpointName }}
  namespace: {{ .Namespace }}
  labels:
    engine: {{ .EngineName }}
    engine_version: {{ .EngineVersion }}
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
            failureThreshold: 3`

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
            failureThreshold: 3`

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
		EngineVersion:   "v0.3.7",
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

var testBase64DeploymentTemplate = `YXBpVmVyc2lvbjogYXBwcy92MQpraW5kOiBEZXBsb3ltZW50Cm1ldGFkYXRhOgogIG5hbWU6IHt7IC5FbmRwb2ludE5hbWUgfX0KICBuYW1lc3BhY2U6IHt7IC5OYW1lc3BhY2UgfX0KICBsYWJlbHM6CiAgICBlbmdpbmU6IHt7IC5FbmdpbmVOYW1lIH19CiAgICBlbmdpbmVfdmVyc2lvbjoge3sgLkVuZ2luZVZlcnNpb24gfX0KICAgIHJvdXRpbmdfbG9naWM6IHt7IC5Sb3V0aW5nTG9naWMgfX0KICAgIGFwcDogaW5mZXJlbmNlCnNwZWM6CiAgcmVwbGljYXM6IHt7IC5SZXBsaWNhcyB9fQogIHByb2dyZXNzRGVhZGxpbmVTZWNvbmRzOiAxMjAwCiAgc3RyYXRlZ3k6CiAgICB0eXBlOiBSb2xsaW5nVXBkYXRlCiAgICByb2xsaW5nVXBkYXRlOgogICAgICBtYXhVbmF2YWlsYWJsZTogMQogICAgICBtYXhTdXJnZTogMAogIHNlbGVjdG9yOgogICAgbWF0Y2hMYWJlbHM6CiAgICAgIGNsdXN0ZXI6IHt7IC5DbHVzdGVyTmFtZSB9fQogICAgICB3b3Jrc3BhY2U6IHt7IC5Xb3Jrc3BhY2UgfX0KICAgICAgZW5kcG9pbnQ6IHt7IC5FbmRwb2ludE5hbWUgfX0KICAgICAgYXBwOiBpbmZlcmVuY2UKICB0ZW1wbGF0ZToKICAgIG1ldGFkYXRhOgogICAgICBsYWJlbHM6CiAgICAgICAgZW5naW5lOiB7eyAuRW5naW5lTmFtZSB9fQogICAgICAgIGVuZ2luZV92ZXJzaW9uOiB7eyAuRW5naW5lVmVyc2lvbiB9fQogICAgICAgIGNsdXN0ZXI6IHt7IC5DbHVzdGVyTmFtZSB9fQogICAgICAgIHdvcmtzcGFjZToge3sgLldvcmtzcGFjZSB9fQogICAgICAgIGVuZHBvaW50OiB7eyAuRW5kcG9pbnROYW1lIH19CiAgICAgICAgcm91dGluZ19sb2dpYzoge3sgLlJvdXRpbmdMb2dpYyB9fQogICAgICAgIGFwcDogaW5mZXJlbmNlCiAgICBzcGVjOgogICAgICBhZmZpbml0eToKICAgICAgICBwb2RBbnRpQWZmaW5pdHk6CiAgICAgICAgICBwcmVmZXJyZWREdXJpbmdTY2hlZHVsaW5nSWdub3JlZER1cmluZ0V4ZWN1dGlvbjoKICAgICAgICAgICAgLSB3ZWlnaHQ6IDEwMAogICAgICAgICAgICAgIHBvZEFmZmluaXR5VGVybToKICAgICAgICAgICAgICAgIGxhYmVsU2VsZWN0b3I6CiAgICAgICAgICAgICAgICAgIG1hdGNoRXhwcmVzc2lvbnM6CiAgICAgICAgICAgICAgICAgICAgLSBrZXk6IGVuZHBvaW50CiAgICAgICAgICAgICAgICAgICAgICBvcGVyYXRvcjogSW4KICAgICAgICAgICAgICAgICAgICAgIHZhbHVlczoKICAgICAgICAgICAgICAgICAgICAgICAgLSB7eyAuRW5kcG9pbnROYW1lIH19CiAgICAgICAgICAgICAgICB0b3BvbG9neUtleTogImt1YmVybmV0ZXMuaW8vaG9zdG5hbWUiCiAgICAgIHt7LSBpZiAuTm9kZVNlbGVjdG9yIH19CiAgICAgIG5vZGVTZWxlY3RvcjoKICAgICAgICB7ey0gcmFuZ2UgJGtleSwgJHZhbHVlIDo9IC5Ob2RlU2VsZWN0b3IgfX0KICAgICAgICB7eyAka2V5IH19OiB7eyAkdmFsdWUgfX0KICAgICAgICB7ey0gZW5kIH19CiAgICAgIHt7LSBlbmQgfX0KICAgICAgY29udGFpbmVyczoKICAgICAgICAtIG5hbWU6IHt7IC5FbmdpbmVOYW1lIH19CiAgICAgICAgICBpbWFnZToge3sgLkltYWdlUHJlZml4IH19L3t7IC5JbWFnZVJlcG8gfX06e3sgLkltYWdlVGFnIH19CiAgICAgICAgICBjb21tYW5kOgogICAgICAgICAgLSB2bGxtCiAgICAgICAgICAtIHNlcnZlCiAgICAgICAgICAtIHt7IC5Nb2RlbEFyZ3MucGF0aCB9fQogICAgICAgICAgLSAtLWhvc3QKICAgICAgICAgIC0gIjAuMC4wLjAiCiAgICAgICAgICAtICItLXBvcnQiCiAgICAgICAgICAtICI4MDAwIgogICAgICAgICAgLSAtLXNlcnZlZC1tb2RlbC1uYW1lCiAgICAgICAgICAtIHt7IC5Nb2RlbEFyZ3Muc2VydmVfbmFtZSB9fQogICAgICAgICAgLSAtLXRhc2sKICAgICAgICAgIHt7LSBpZiBlcSAuTW9kZWxBcmdzLnRhc2sgInRleHQtZW1iZWRkaW5nIiB9fQogICAgICAgICAgLSBlbWJlZGRpbmcKICAgICAgICAgIHt7LSBlbHNlIGlmIGVxIC5Nb2RlbEFyZ3MudGFzayAidGV4dC1nZW5lcmF0aW9uIiB9fQogICAgICAgICAgLSBnZW5lcmF0ZQogICAgICAgICAge3stIGVsc2UgaWYgZXEgLk1vZGVsQXJncy50YXNrICJ0ZXh0LXJlcmFuayIgfX0KICAgICAgICAgIC0gcmVyYW5rCiAgICAgICAgICB7ey0gZWxzZSB9fQogICAgICAgICAgLSB7eyAuTW9kZWxBcmdzLnRhc2sgfX0KICAgICAgICAgIHt7LSBlbmQgfX0KICAgICAgICAgIHt7LSBpZiAuRW5naW5lQXJncyB9fQogICAgICAgICAge3stIHJhbmdlICRrZXksICR2YWx1ZSA6PSAuRW5naW5lQXJncyB9fQogICAgICAgICAgLSAtLXt7ICRrZXkgfX0KICAgICAge3stIGlmIG5lIChwcmludGYgIiV2IiAkdmFsdWUpICJ0cnVlIn19CiAgICAgICAgICAtICJ7eyAkdmFsdWUgfX0iCiAgICAgIHt7LSBlbmQgfX0KICAgICAgICAgIHt7LSBlbmQgfX0KICAgICAgICAgIHt7LSBlbmQgfX0KICAgICAgICAgIHJlc291cmNlczoKICAgICAgICAgICAgbGltaXRzOgogICAgICAgICAgICAgIHt7LSByYW5nZSAka2V5LCAkdmFsdWUgOj0gLlJlc291cmNlcyB9fQogICAgICAgICAgICAgIHt7ICRrZXkgfX06IHt7ICR2YWx1ZSB9fQogICAgICAgICAgICAgIHt7LSBlbmQgfX0KICAgICAgICAgICAgcmVxdWVzdHM6CiAgICAgICAgICAgICAge3stIHJhbmdlICRrZXksICR2YWx1ZSA6PSAuUmVzb3VyY2VzIH19CiAgICAgICAgICAgICAge3sgJGtleSB9fToge3sgJHZhbHVlIH19CiAgICAgICAgICAgICAge3stIGVuZCB9fQogICAgICAgICAgZW52OgogICAgICAgICAgIHt7IHJhbmdlICRrZXksICR2YWx1ZSA6PSAuRW52IH19CiAgICAgICAgICAgLSBuYW1lOiB7eyAka2V5IH19CiAgICAgICAgICAgICB2YWx1ZTogInt7ICR2YWx1ZSB9fSIKICAgICAgICAgICB7eyBlbmQgfX0KICAgICAgICAgIHBvcnRzOgogICAgICAgICAgICAtIGNvbnRhaW5lclBvcnQ6IDgwMDAKICAgICAgICAgIHN0YXJ0dXBQcm9iZToKICAgICAgICAgICAgaHR0cEdldDoKICAgICAgICAgICAgICBwYXRoOiAvaGVhbHRoCiAgICAgICAgICAgICAgcG9ydDogODAwMAogICAgICAgICAgICBpbml0aWFsRGVsYXlTZWNvbmRzOiA1CiAgICAgICAgICAgIHRpbWVvdXRTZWNvbmRzOiA1CiAgICAgICAgICAgIHBlcmlvZFNlY29uZHM6IDEwCiAgICAgICAgICAgIHN1Y2Nlc3NUaHJlc2hvbGQ6IDEKICAgICAgICAgICAgZmFpbHVyZVRocmVzaG9sZDogMTIwCiAgICAgICAgICByZWFkaW5lc3NQcm9iZToKICAgICAgICAgICAgaHR0cEdldDoKICAgICAgICAgICAgICBwYXRoOiAvaGVhbHRoCiAgICAgICAgICAgICAgcG9ydDogODAwMAogICAgICAgICAgICBpbml0aWFsRGVsYXlTZWNvbmRzOiA1CiAgICAgICAgICAgIHRpbWVvdXRTZWNvbmRzOiA1CiAgICAgICAgICAgIHBlcmlvZFNlY29uZHM6IDEwCiAgICAgICAgICAgIHN1Y2Nlc3NUaHJlc2hvbGQ6IDEKICAgICAgICAgICAgZmFpbHVyZVRocmVzaG9sZDogMw==`

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

	data := newDeploymentManifestVariables()
	k.setBasicVariables(&data, endpoint, cluster, engine)

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
		engine       *v1.Engine
		endpoint     *v1.Endpoint
		expectedArgs map[string]interface{}
	}{
		{
			name: "with engine args",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "vllm",
				},
			},
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
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "vllm",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Variables: map[string]interface{}{
						"other_var": "value",
					},
				},
			},
			expectedArgs: map[string]interface{}{},
		},
		{
			name: "with nil variables",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "vllm",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{},
			},
			expectedArgs: map[string]interface{}{},
		},
		{
			name: "llama-cpp engine with default interrupt_requests",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "llama-cpp",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{},
			},
			expectedArgs: map[string]interface{}{
				"interrupt_requests": "false",
			},
		},
		{
			name: "llama-cpp engine with user override",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "llama-cpp",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Variables: map[string]interface{}{
						"engine_args": map[string]interface{}{
							"interrupt_requests": "true",
							"n_ctx":              "2048",
						},
					},
				},
			},
			expectedArgs: map[string]interface{}{
				"interrupt_requests": "true",
				"n_ctx":              "2048",
			},
		},
		{
			name: "map values are YAML-escaped for template rendering",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "vllm",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Variables: map[string]interface{}{
						"engine_args": map[string]interface{}{
							"speculative-config": map[string]interface{}{"method": "mtp"},
							"max-model-len":      "4096",
						},
					},
				},
			},
			expectedArgs: map[string]interface{}{
				"speculative-config": `{\"method\":\"mtp\"}`,
				"max-model-len":      "4096",
			},
		},
		{
			name: "vllm engine with GPU > 1 should auto-set tensor-parallel-size",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "vllm",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Resources: &v1.ResourceSpec{
						GPU: pointer.String("4"),
					},
				},
			},
			expectedArgs: map[string]interface{}{
				"tensor_parallel_size": 4,
			},
		},
		{
			name: "vllm engine with GPU <= 1 should not set tensor-parallel-size",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "vllm",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Resources: &v1.ResourceSpec{
						GPU: pointer.String("1"),
					},
				},
			},
			expectedArgs: map[string]interface{}{},
		},
		{
			name: "vllm engine user-provided tensor-parallel-size overrides default",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "vllm",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Resources: &v1.ResourceSpec{
						GPU: pointer.String("4"),
					},
					Variables: map[string]interface{}{
						"engine_args": map[string]interface{}{
							"tensor-parallel-size": "2",
						},
					},
				},
			},
			expectedArgs: map[string]interface{}{
				"tensor-parallel-size": "2",
			},
		},
		{
			name: "vllm engine user-provided underscore key prevents default",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "vllm",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Resources: &v1.ResourceSpec{
						GPU: pointer.String("4"),
					},
					Variables: map[string]interface{}{
						"engine_args": map[string]interface{}{
							"tensor_parallel_size": "1",
						},
					},
				},
			},
			expectedArgs: map[string]interface{}{
				"tensor_parallel_size": "1",
			},
		},
		{
			name: "vllm engine with nil resources should not set tensor-parallel-size",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "vllm",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{},
			},
			expectedArgs: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := newDeploymentManifestVariables()
			k.setEngineArgs(&data, tt.endpoint, tt.engine)
			assert.Equal(t, tt.expectedArgs, data.EngineArgs)
		})
	}
}

func TestEscapeEngineArgsForTemplate(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name: "native map is JSON-serialized and YAML-escaped",
			args: map[string]interface{}{
				"speculative-config": map[string]interface{}{"method": "mtp"},
			},
			expected: map[string]interface{}{
				"speculative-config": `{\"method\":\"mtp\"}`,
			},
		},
		{
			name: "native slice is JSON-serialized and YAML-escaped",
			args: map[string]interface{}{
				"allowed-origins": []interface{}{"http://localhost", "https://example.com"},
			},
			expected: map[string]interface{}{
				"allowed-origins": `[\"http://localhost\",\"https://example.com\"]`,
			},
		},
		{
			name: "unescaped JSON string is YAML-escaped",
			args: map[string]interface{}{
				"speculative-config": `{"method":"mtp"}`,
			},
			expected: map[string]interface{}{
				"speculative-config": `{\"method\":\"mtp\"}`,
			},
		},
		{
			name: "pre-escaped string is left as-is",
			args: map[string]interface{}{
				"speculative-config": `{\"method\":\"mtp\"}`,
			},
			expected: map[string]interface{}{
				"speculative-config": `{\"method\":\"mtp\"}`,
			},
		},
		{
			name: "simple string is unchanged",
			args: map[string]interface{}{
				"dtype": "float16",
			},
			expected: map[string]interface{}{
				"dtype": "float16",
			},
		},
		{
			name: "numeric string is unchanged",
			args: map[string]interface{}{
				"max-model-len": "4096",
			},
			expected: map[string]interface{}{
				"max-model-len": "4096",
			},
		},
		{
			name: "mixed values",
			args: map[string]interface{}{
				"speculative-config": map[string]interface{}{"method": "mtp"},
				"max-model-len":      "4096",
				"override-config":    `{"temperature":0.5}`,
				"dtype":              "float16",
			},
			expected: map[string]interface{}{
				"speculative-config": `{\"method\":\"mtp\"}`,
				"max-model-len":      "4096",
				"override-config":    `{\"temperature\":0.5}`,
				"dtype":              "float16",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			escapeEngineArgsForTemplate(tt.args)
			assert.Equal(t, tt.expected, tt.args)
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
			data := newDeploymentManifestVariables()
			k.setEnvironmentVariables(&data, tt.endpoint)
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
			data := newDeploymentManifestVariables()
			k.setModelArgs(&data, tt.endpoint, tt.modelRegistry)
			assert.Equal(t, tt.expectedModelArgs, data.ModelArgs)
		})
	}
}

func TestKubernetesOrchestrator_addSharedMemoryVolume(t *testing.T) {
	k := &kubernetesOrchestrator{}

	data := newDeploymentManifestVariables()
	k.addSharedMemoryVolume(&data)

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
	modelPathPrefix := filepath.Join(v1.DefaultK8sClusterModelCacheMountPath, v1.DefaultModelCacheRelativePath)
	tests := []struct {
		name          string
		modelRegistry *v1.ModelRegistry
		endpoint      *v1.Endpoint
		cluster       *v1.Cluster
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
			cluster: &v1.Cluster{},
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
					"version":       "", // Empty version for HuggingFace to use default branch
				},
				Env: map[string]string{
					v1.HFEndpoint: "https://huggingface.co",
					v1.HFTokenEnv: "hf_test_token",
				},
			},
		},
		{
			name: "HuggingFace with special version",
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
			cluster: &v1.Cluster{},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Model: &v1.ModelSpec{
						Name:    "test-model",
						Version: "v1",
					},
				},
			},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"registry_path": "test-model",
					"path":          filepath.Join(modelPathPrefix, "test-model", "v1"),
					"version":       "v1",
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
			cluster: &v1.Cluster{},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"registry_path": "test-model",
					"path":          filepath.Join(modelPathPrefix, "test-model"),
					"version":       "", // Empty version for HuggingFace to use default branch
				},
				Env: map[string]string{
					v1.HFEndpoint: "https://huggingface.co",
				},
			},
		},
		{
			name: "HuggingFace without credentials - Cluster with model cache",
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
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{
					Config: &v1.ClusterConfig{
						KubernetesConfig: &v1.KubernetesClusterConfig{},
						ModelCaches: []v1.ModelCache{
							{
								Name:     "test-cache",
								HostPath: &corev1.HostPathVolumeSource{},
							},
						},
					},
				},
			},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"registry_path": "test-model",
					"path":          filepath.Join(v1.DefaultK8sClusterModelCacheMountPath, "test-cache", "test-model"),
					"version":       "", // Empty version for HuggingFace to use default branch
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

			err := k.setModelRegistryVariables(data, tt.endpoint, tt.cluster, tt.modelRegistry)
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
	modelPathPrefix := filepath.Join(v1.DefaultK8sClusterModelCacheMountPath, v1.DefaultModelCacheRelativePath)
	k := &kubernetesOrchestrator{}

	tests := []struct {
		name          string
		modelRegistry *v1.ModelRegistry
		endpoint      *v1.Endpoint
		cluster       *v1.Cluster
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
			cluster: &v1.Cluster{},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"path":          filepath.Join(modelPathPrefix, "llama-2-7b", "v1.0"),
					"registry_path": "/mnt/bentoml/models/llama-2-7b/v1.0",
					"version":       "v1.0",
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
				Env: map[string]string{},
			},
			expectError: false,
		},
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
			cluster: &v1.Cluster{},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"path":          filepath.Join(modelPathPrefix, "llama-2-7b", "v1.0"),
					"registry_path": "/mnt/bentoml/models/llama-2-7b/v1.0",
					"version":       "v1.0",
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
				Env: map[string]string{},
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
			cluster: &v1.Cluster{},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"path":          filepath.Join(modelPathPrefix, "gpt-model/v2.0"),
					"registry_path": "/mnt/bentoml/models/gpt-model/v2.0",
					"version":       "v2.0",
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
				Env: map[string]string{},
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
			cluster: &v1.Cluster{},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{},
				Env:       map[string]string{},
			},
			expectError: false,
		},
		{
			name: "BentoML with NFS - specific version - deployed cluster with model cache",
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
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{
					Config: &v1.ClusterConfig{
						KubernetesConfig: &v1.KubernetesClusterConfig{},
						ModelCaches: []v1.ModelCache{
							{
								Name:     "test-cache",
								HostPath: &corev1.HostPathVolumeSource{},
							},
						},
					},
				},
			},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"path":          filepath.Join(v1.DefaultK8sClusterModelCacheMountPath, "test-cache", "llama-2-7b", "v1.0"),
					"registry_path": "/mnt/bentoml/models/llama-2-7b/v1.0",
					"version":       "v1.0",
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
				Env: map[string]string{},
			},
			expectError: false,
		},
		{
			name: "BentoML with NFS - specific version - deployed cluster with multi model cache - only use the first cache name as relative path",
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
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{
					Config: &v1.ClusterConfig{
						KubernetesConfig: &v1.KubernetesClusterConfig{},
						ModelCaches: []v1.ModelCache{
							{
								Name:     "test-cache-1",
								HostPath: &corev1.HostPathVolumeSource{},
							},
							{
								Name:     "test-cache-2",
								HostPath: &corev1.HostPathVolumeSource{},
							},
						},
					},
				},
			},
			expected: &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{
					"path":          filepath.Join(v1.DefaultK8sClusterModelCacheMountPath, "test-cache-1", "llama-2-7b", "v1.0"),
					"registry_path": "/mnt/bentoml/models/llama-2-7b/v1.0",
					"version":       "v1.0",
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
				Env: map[string]string{},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := &DeploymentManifestVariables{
				ModelArgs: map[string]interface{}{},
				Env:       map[string]string{},
			}

			err := k.setModelRegistryVariables(data, tt.endpoint, tt.cluster, tt.modelRegistry)
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
						CPU: pointer.String("4.0"),
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
			data := newDeploymentManifestVariables()

			err := k.setDeployImageVariables(&data, tt.endpoint, tt.engine, tt.imageRegistry)

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
					Name: "test-cache",
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/data/huggingface",
					},
				},
			},
			expectedEnvs: map[string]string{},
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
					Name: "models-cache-test-cache",
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
					Name:      "models-cache-test-cache",
					MountPath: "/models-cache/test-cache",
				},
			},
		},
		{
			name: "with pvc model cache",
			modelCaches: []v1.ModelCache{
				{
					Name: "test-pvc",
					PVC:  &corev1.PersistentVolumeClaimSpec{},
				},
			},
			expectedEnvs: map[string]string{},
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
					Name: "models-cache-test-pvc",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: "models-cache-test-pvc",
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
					Name:      "models-cache-test-pvc",
					MountPath: "/models-cache/test-pvc",
				},
			},
		},
		{
			name: "multiple model caches",
			modelCaches: []v1.ModelCache{
				{
					Name: "test-cache-1",
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/data/huggingface",
					},
				},
				{
					Name: "test-cache-2",
					NFS: &corev1.NFSVolumeSource{
						Server: "192.168.1.1",
						Path:   "/models",
					},
				},
			},
			expectedEnvs: map[string]string{},
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
					Name: "models-cache-test-cache-1",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/data/huggingface",
						},
					},
				},
				{
					Name: "models-cache-test-cache-2",
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
					Name:      "models-cache-test-cache-1",
					MountPath: "/models-cache/test-cache-1",
				},
				{
					Name:      "models-cache-test-cache-2",
					MountPath: "/models-cache/test-cache-2",
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

func TestKubernetesOrchestrator_getEndpointStats(t *testing.T) {
	newEndpoint := func() *v1.Endpoint {
		return &v1.Endpoint{
			Metadata: &v1.Metadata{
				Workspace: "production",
				Name:      "chat-model",
			},
			Spec: &v1.EndpointSpec{
				Replicas: v1.ReplicaSpec{
					Num: pointer.Int(1),
				},
			},
		}
	}

	tests := []struct {
		name           string
		inputEndpoint  func() *v1.Endpoint
		setupMock      func(*testing.T) *FakeK8sClient
		expectedPhase  v1.EndpointPhase
		expectErrorMsg string
		expectError    bool
	}{
		{
			name: "return error if deployment fetch fails",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).WithGetError(assert.AnError)
			},
			expectError: true,
		},
		{
			name: "return Deleted for non-existing deployment when deletion timestamp is set",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Metadata.DeletionTimestamp = "2024-01-01T00:00:00Z"
				return ep
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).WithDeploymentNotFound()
			},
			expectedPhase: v1.EndpointPhaseDELETED,
			expectError:   false,
		},
		{
			name: "return Deleting when deployment gone but pods still terminating",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Metadata.DeletionTimestamp = "2024-01-01T00:00:00Z"
				return ep
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				// No deployment, but pods still exist (terminating)
				return NewFakeK8sClient(t).WithPods(1)
			},
			expectedPhase:  v1.EndpointPhaseDELETING,
			expectErrorMsg: "waiting for 1 pod(s) to terminate",
			expectError:    false,
		},
		{
			name: "return Deleting for existing deployment with pods when deletion timestamp is set",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Metadata.DeletionTimestamp = "2024-01-01T00:00:00Z"
				return ep
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 1, 1).
					WithPods(1)
			},
			expectedPhase:  v1.EndpointPhaseDELETING,
			expectErrorMsg: "waiting for 1 pod(s) to terminate",
			expectError:    false,
		},
		{
			name: "return Deploying for deployment not found when not deleting",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).WithDeploymentNotFound()
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "Endpoint deployment not found",
			expectError:    false,
		},
		{
			name: "return Deploying for endpoint with zero replicas but pods still exist",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Spec.Replicas.Num = pointer.Int(0)
				return ep
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 0, 0, 0).
					WithPods(1) // Still has pods
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "waiting for all pods to terminate",
			expectError:    false,
		},
		{
			name: "return Paused for endpoint with zero replicas and no pods",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Spec.Replicas.Num = pointer.Int(0)
				return ep
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 0, 0, 0).
					WithPods(0)
			},
			expectedPhase: v1.EndpointPhasePAUSED,
			expectError:   false,
		},
		{
			name: "return Running for deployment with all replicas ready",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 1, 1).
					WithPods(1)
			},
			expectedPhase: v1.EndpointPhaseRUNNING,
			expectError:   false,
		},
		{
			name: "return Deploying for deployment with not all replicas ready",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithPods(1)
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "Deployment: 0/1 replicas ready",
			expectError:    false,
		},
		{
			name: "return Failed for pod in CrashLoopBackOff with high restart count",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithPodInCrashLoopBackOff("test-container", 5)
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "CrashLoopBackOff",
			expectError:    false,
		},
		{
			name: "return Failed for pod with ImagePullBackOff",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithPodInImagePullBackOff("test-container")
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "failed to pull image",
			expectError:    false,
		},
		{
			name: "return Failed for pod with OOMKilled",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithPodOOMKilled("test-container")
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "OOM (Out of Memory)",
			expectError:    false,
		},
		{
			name: "return Failed for unschedulable pod",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithUnschedulablePod("Insufficient cpu")
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "unschedulable",
			expectError:    false,
		},
		{
			name: "return Deploying with deployment conditions message",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeploymentWithCondition(newEndpoint().Metadata.Name, 1, 0, 0, "Progressing", "NewReplicaSetAvailable", "ReplicaSet is progressing")
			},
			expectedPhase:  v1.EndpointPhaseDEPLOYING,
			expectErrorMsg: "ReplicaSet is progressing",
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := tt.setupMock(t)

			o := &kubernetesOrchestrator{}

			status, err := o.getEndpointStats(fakeClient, "test-namespace", tt.inputEndpoint())

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedPhase, status.Phase)
				if tt.expectErrorMsg != "" {
					assert.Contains(t, status.ErrorMessage, tt.expectErrorMsg)
				}
			}

			fakeClient.AssertExpectations()
		})
	}
}

func TestInjectInfrastructure(t *testing.T) {
	// Build template objects (engine-only, no infra)
	data := DeploymentManifestVariables{
		ClusterName:     "test-cluster",
		Workspace:       "test-workspace",
		Namespace:       "default",
		ImagePrefix:     "registry.example.com",
		ImageRepo:       "vllm",
		ImageTag:        "v0.11.2",
		ImagePullSecret: "my-secret",
		EngineName:      "vllm",
		EngineVersion:   "v0.11.2",
		EndpointName:    "test-endpoint",
		NeutreeVersion:  "v1.0.0",
		ModelArgs: map[string]interface{}{
			"name":          "gpt-4",
			"task":          "text-generation",
			"path":          "/mnt/models/gpt-4",
			"registry_type": "huggingface",
			"registry_path": "gpt-4",
			"serve_name":    "gpt-4",
			"version":       "latest",
			"file":          "",
		},
		EngineArgs: map[string]interface{}{},
		Resources: map[string]string{
			"nvidia.com/gpu": "1",
		},
		Env: map[string]string{
			"HF_TOKEN": "test-token",
		},
		RoutingLogic: "roundrobin",
		Replicas:     1,
		Volumes: []corev1.Volume{
			{
				Name: "model-cache",
				VolumeSource: corev1.VolumeSource{
					NFS: &corev1.NFSVolumeSource{
						Server: "10.0.0.1",
						Path:   "/models",
					},
				},
			},
			{
				Name: "dshm",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						Medium: corev1.StorageMediumMemory,
					},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "model-cache", MountPath: "/mnt/models"},
			{Name: "dshm", MountPath: "/dev/shm"},
		},
	}

	// Render template (engine-only)
	templateObjects, err := buildDeploymentObjects(testVllmDeploymentTemplate, data)
	require.NoError(t, err)
	require.Len(t, templateObjects.Items, 1)

	// Verify template has NO infra
	templateDep := &appsv1.Deployment{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(templateObjects.Items[0].Object, templateDep)
	require.NoError(t, err)
	assert.Empty(t, templateDep.Spec.Template.Spec.InitContainers, "template should have no initContainers")
	assert.Empty(t, templateDep.Spec.Template.Spec.ImagePullSecrets, "template should have no imagePullSecrets")
	assert.Empty(t, templateDep.Spec.Template.Spec.Volumes, "template should have no volumes")
	assert.Empty(t, templateDep.Spec.Template.Spec.Containers[0].VolumeMounts, "template engine container should have no volumeMounts")

	// Deep copy and inject infrastructure
	fullObjects := templateObjects.DeepCopy()
	err = injectInfrastructure(fullObjects, data)
	require.NoError(t, err)

	// Verify full objects have infra injected
	fullDep := &appsv1.Deployment{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(fullObjects.Items[0].Object, fullDep)
	require.NoError(t, err)

	// Check imagePullSecrets
	require.Len(t, fullDep.Spec.Template.Spec.ImagePullSecrets, 1)
	assert.Equal(t, "my-secret", fullDep.Spec.Template.Spec.ImagePullSecrets[0].Name)

	// Check volumes
	assert.Len(t, fullDep.Spec.Template.Spec.Volumes, 2)
	assert.Equal(t, "model-cache", fullDep.Spec.Template.Spec.Volumes[0].Name)
	assert.Equal(t, "dshm", fullDep.Spec.Template.Spec.Volumes[1].Name)

	// Check initContainers
	require.Len(t, fullDep.Spec.Template.Spec.InitContainers, 1)
	initContainer := fullDep.Spec.Template.Spec.InitContainers[0]
	assert.Equal(t, "model-downloader", initContainer.Name)
	assert.Equal(t, "registry.example.com/neutree/neutree-runtime:v1.0.0", initContainer.Image)
	assert.Equal(t, []string{"python3"}, initContainer.Command)
	assert.Contains(t, initContainer.Args, "-m")
	assert.Contains(t, initContainer.Args, "neutree.downloader")
	assert.Contains(t, initContainer.Args, "--name")
	assert.Contains(t, initContainer.Args, "gpt-4")

	// Check initContainer env
	assert.Len(t, initContainer.Env, 1)
	assert.Equal(t, "HF_TOKEN", initContainer.Env[0].Name)
	assert.Equal(t, "test-token", initContainer.Env[0].Value)

	// Check initContainer volumeMounts
	assert.Len(t, initContainer.VolumeMounts, 2)

	// Check engine container volumeMounts
	engineContainer := fullDep.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "vllm", engineContainer.Name)
	assert.Len(t, engineContainer.VolumeMounts, 2)
	assert.Equal(t, "model-cache", engineContainer.VolumeMounts[0].Name)
	assert.Equal(t, "dshm", engineContainer.VolumeMounts[1].Name)

	// Verify template objects are NOT modified (deep copy isolation)
	templateDep2 := &appsv1.Deployment{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(templateObjects.Items[0].Object, templateDep2)
	require.NoError(t, err)
	assert.Empty(t, templateDep2.Spec.Template.Spec.InitContainers, "template should still have no initContainers after injection")
	assert.Empty(t, templateDep2.Spec.Template.Spec.Volumes, "template should still have no volumes after injection")
}

func TestInfraChangesDoNotCauseDiff(t *testing.T) {
	// Build template objects with NeutreeVersion v1.0.0
	data := DeploymentManifestVariables{
		ClusterName:     "test-cluster",
		Workspace:       "test-workspace",
		Namespace:       "default",
		ImagePrefix:     "registry.example.com",
		ImageRepo:       "vllm",
		ImageTag:        "v0.11.2",
		ImagePullSecret: "my-secret",
		EngineName:      "vllm",
		EngineVersion:   "v0.11.2",
		EndpointName:    "test-endpoint",
		NeutreeVersion:  "v1.0.0",
		ModelArgs: map[string]interface{}{
			"name": "gpt-4", "task": "text-generation", "path": "/mnt/models/gpt-4",
			"registry_type": "huggingface", "registry_path": "gpt-4",
			"serve_name": "gpt-4", "version": "latest", "file": "",
		},
		EngineArgs:   map[string]interface{}{},
		Resources:    map[string]string{"nvidia.com/gpu": "1"},
		Env:          map[string]string{},
		RoutingLogic: "roundrobin",
		Replicas:     1,
		Volumes:      []corev1.Volume{},
		VolumeMounts: []corev1.VolumeMount{},
	}

	// Render template objects (used for diff baseline)
	templateV1, err := buildDeploymentObjects(testVllmDeploymentTemplate, data)
	require.NoError(t, err)

	// Simulate cluster version upgrade: NeutreeVersion changes to v2.0.0
	data.NeutreeVersion = "v2.0.0"

	// Re-render template — since template doesn't use NeutreeVersion, output is identical
	templateV2, err := buildDeploymentObjects(testVllmDeploymentTemplate, data)
	require.NoError(t, err)

	// Verify spec hashes are identical (no diff)
	hash1 := specHash(t, &templateV1.Items[0])
	hash2 := specHash(t, &templateV2.Items[0])
	assert.Equal(t, hash1, hash2, "template objects should be identical regardless of NeutreeVersion")
}

func TestUserChangeDoesCauseDiff(t *testing.T) {
	baseData := DeploymentManifestVariables{
		ClusterName:     "test-cluster",
		Workspace:       "test-workspace",
		Namespace:       "default",
		ImagePrefix:     "registry.example.com",
		ImageRepo:       "vllm",
		ImageTag:        "v0.11.2",
		ImagePullSecret: "my-secret",
		EngineName:      "vllm",
		EngineVersion:   "v0.11.2",
		EndpointName:    "test-endpoint",
		NeutreeVersion:  "v1.0.0",
		ModelArgs: map[string]interface{}{
			"name": "gpt-4", "task": "text-generation", "path": "/mnt/models/gpt-4",
			"registry_type": "huggingface", "registry_path": "gpt-4",
			"serve_name": "gpt-4", "version": "latest", "file": "",
		},
		EngineArgs:   map[string]interface{}{},
		Resources:    map[string]string{"nvidia.com/gpu": "1"},
		Env:          map[string]string{},
		RoutingLogic: "roundrobin",
		Replicas:     1,
		Volumes:      []corev1.Volume{},
		VolumeMounts: []corev1.VolumeMount{},
	}

	templateBefore, err := buildDeploymentObjects(testVllmDeploymentTemplate, baseData)
	require.NoError(t, err)

	// User changes replicas
	baseData.Replicas = 3
	templateAfter, err := buildDeploymentObjects(testVllmDeploymentTemplate, baseData)
	require.NoError(t, err)

	hashBefore := specHash(t, &templateBefore.Items[0])
	hashAfter := specHash(t, &templateAfter.Items[0])
	assert.NotEqual(t, hashBefore, hashAfter, "user changes (replicas) should cause diff")
}

// specHash computes a SHA256 hash of the spec field, mirroring deploy.computeSpecHash.
func specHash(t *testing.T, obj *unstructured.Unstructured) string {
	t.Helper()

	spec, found := obj.Object["spec"]
	if !found {
		spec = obj.Object
	}

	specJSON, err := json.Marshal(spec)
	require.NoError(t, err)

	hash := sha256.Sum256(specJSON)

	return fmt.Sprintf("%x", hash)
}
