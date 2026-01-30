package orchestrator

import (
	"context"
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
          image: {{ .ImagePrefix }}/neutree/neutree-runtime:{{ .NeutreeVersion }}
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
          image: {{ .ImagePrefix }}/neutree/neutree-runtime:{{ .NeutreeVersion }}
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

var testBase64DeploymentTemplate = `YXBpVmVyc2lvbjogYXBwcy92MQpraW5kOiBEZXBsb3ltZW50Cm1ldGFkYXRhOgogIG5hbWU6IHt7IC5FbmRwb2ludE5hbWUgfX0KICBuYW1lc3BhY2U6IHt7IC5OYW1lc3BhY2UgfX0KICBsYWJlbHM6CiAgICBlbmdpbmU6IHt7IC5FbmdpbmVOYW1lIH19CiAgICBlbmdpbmVfdmVyc2lvbjoge3sgLkVuZ2luZVZlcnNpb24gfX0KICAgIGNsdXN0ZXI6IHt7IC5DbHVzdGVyTmFtZSB9fQogICAgd29ya3NwYWNlOiB7eyAuV29ya3NwYWNlIH19CiAgICBlbmRwb2ludDoge3sgLkVuZHBvaW50TmFtZSB9fQogICAgcm91dGluZ19sb2dpYzoge3sgLlJvdXRpbmdMb2dpYyB9fQogICAgYXBwOiBpbmZlcmVuY2UKc3BlYzoKICByZXBsaWNhczoge3sgLlJlcGxpY2FzIH19CiAgcHJvZ3Jlc3NEZWFkbGluZVNlY29uZHM6IDEyMDAKICBzdHJhdGVneToKICAgIHR5cGU6IFJvbGxpbmdVcGRhdGUKICAgIHJvbGxpbmdVcGRhdGU6CiAgICAgIG1heFVuYXZhaWxhYmxlOiAxCiAgICAgIG1heFN1cmdlOiAwCiAgc2VsZWN0b3I6CiAgICBtYXRjaExhYmVsczoKICAgICAgY2x1c3Rlcjoge3sgLkNsdXN0ZXJOYW1lIH19CiAgICAgIHdvcmtzcGFjZToge3sgLldvcmtzcGFjZSB9fQogICAgICBlbmRwb2ludDoge3sgLkVuZHBvaW50TmFtZSB9fQogICAgICBhcHA6IGluZmVyZW5jZQogIHRlbXBsYXRlOgogICAgbWV0YWRhdGE6CiAgICAgIGxhYmVsczoKICAgICAgICBlbmdpbmU6IHt7IC5FbmdpbmVOYW1lIH19CiAgICAgICAgZW5naW5lX3ZlcnNpb246IHt7IC5FbmdpbmVWZXJzaW9uIH19CiAgICAgICAgY2x1c3Rlcjoge3sgLkNsdXN0ZXJOYW1lIH19CiAgICAgICAgd29ya3NwYWNlOiB7eyAuV29ya3NwYWNlIH19CiAgICAgICAgZW5kcG9pbnQ6IHt7IC5FbmRwb2ludE5hbWUgfX0KICAgICAgICByb3V0aW5nX2xvZ2ljOiB7eyAuUm91dGluZ0xvZ2ljIH19CiAgICAgICAgYXBwOiBpbmZlcmVuY2UKICAgIHNwZWM6CiAgICAgIGFmZmluaXR5OgogICAgICAgIHBvZEFudGlBZmZpbml0eToKICAgICAgICAgIHByZWZlcnJlZER1cmluZ1NjaGVkdWxpbmdJZ25vcmVkRHVyaW5nRXhlY3V0aW9uOgogICAgICAgICAgICAtIHdlaWdodDogMTAwCiAgICAgICAgICAgICAgcG9kQWZmaW5pdHlUZXJtOgogICAgICAgICAgICAgICAgbGFiZWxTZWxlY3RvcjoKICAgICAgICAgICAgICAgICAgbWF0Y2hFeHByZXNzaW9uczoKICAgICAgICAgICAgICAgICAgICAtIGtleTogZW5kcG9pbnQKICAgICAgICAgICAgICAgICAgICAgIG9wZXJhdG9yOiBJbgogICAgICAgICAgICAgICAgICAgICAgdmFsdWVzOgogICAgICAgICAgICAgICAgICAgICAgICAtIHt7IC5FbmRwb2ludE5hbWUgfX0KICAgICAgICAgICAgICAgIHRvcG9sb2d5S2V5OiAia3ViZXJuZXRlcy5pby9ob3N0bmFtZSIKICAgICAge3stIGlmIC5Ob2RlU2VsZWN0b3IgfX0KICAgICAgbm9kZVNlbGVjdG9yOgogICAgICAgIHt7LSByYW5nZSAka2V5LCAkdmFsdWUgOj0gLk5vZGVTZWxlY3RvciB9fQogICAgICAgIHt7ICRrZXkgfX06IHt7ICR2YWx1ZSB9fQogICAgICAgIHt7LSBlbmQgfX0KICAgICAge3stIGVuZCB9fQogICAgICB7ey0gaWYgLkltYWdlUHVsbFNlY3JldCB9fQogICAgICBpbWFnZVB1bGxTZWNyZXRzOgogICAgICAgIC0gbmFtZToge3sgLkltYWdlUHVsbFNlY3JldCB9fQogICAgICB7ey0gZW5kIH19CgogICAgICB7ey0gaWYgLlZvbHVtZXMgfX0KICAgICAgdm9sdW1lczoKe3sgLlZvbHVtZXMgfCB0b1lhbWwgfCBpbmRlbnQgNiB9fQogICAgICB7ey0gZW5kIH19CiAgICAgIGluaXRDb250YWluZXJzOgogICAgICAgIC0gbmFtZTogbW9kZWwtZG93bmxvYWRlcgogICAgICAgICAgaW1hZ2U6IHt7IC5JbWFnZVByZWZpeCB9fS9uZXV0cmVlL25ldXRyZWUtcnVudGltZTp7eyAuTmV1dHJlZVZlcnNpb24gfX0KICAgICAgICAgIGNvbW1hbmQ6CiAgICAgICAgICAgIC0gYmFzaAogICAgICAgICAgICAtIC1jCiAgICAgICAgICBhcmdzOgogICAgICAgICAgICAtID4tCiAgICAgICAgICAgICAgcHl0aG9uMyAtbSBuZXV0cmVlLmRvd25sb2FkZXIKICAgICAgICAgICAgICAtLW5hbWU9Int7IC5Nb2RlbEFyZ3MubmFtZSB9fSIKICAgICAgICAgICAgICAtLXJlZ2lzdHJ5X3R5cGU9Int7IC5Nb2RlbEFyZ3MucmVnaXN0cnlfdHlwZSB9fSIKICAgICAgICAgICAgICAtLXJlZ2lzdHJ5X3BhdGg9Int7IC5Nb2RlbEFyZ3MucmVnaXN0cnlfcGF0aCB9fSIKICAgICAgICAgICAgICAtLXZlcnNpb249Int7IC5Nb2RlbEFyZ3MudmVyc2lvbiB9fSIKICAgICAgICAgICAgICAtLWZpbGU9Int7IC5Nb2RlbEFyZ3MuZmlsZSB9fSIKICAgICAgICAgICAgICAtLXRhc2s9Int7IC5Nb2RlbEFyZ3MudGFzayB9fSIKICAgICAgICAgIGVudjoKICAgICAgICAgICB7eyByYW5nZSAka2V5LCAkdmFsdWUgOj0gLkVudiB9fQogICAgICAgICAgIC0gbmFtZToge3sgJGtleSB9fQogICAgICAgICAgICAgdmFsdWU6ICJ7eyAkdmFsdWUgfX0iCiAgICAgICAgICAge3sgZW5kIH19CiAgICAgICAgICB7ey0gaWYgLlZvbHVtZU1vdW50cyB9fQogICAgICAgICAgdm9sdW1lTW91bnRzOgp7eyAuVm9sdW1lTW91bnRzIHwgdG9ZYW1sIHwgaW5kZW50IDEwIH19CiAgICAgICAgICB7ey0gZW5kIH19CgogICAgICBjb250YWluZXJzOgogICAgICAgIC0gbmFtZToge3sgLkVuZ2luZU5hbWUgfX0KICAgICAgICAgIGltYWdlOiB7eyAuSW1hZ2VQcmVmaXggfX0ve3sgLkltYWdlUmVwbyB9fTp7eyAuSW1hZ2VUYWcgfX0KICAgICAgICAgIGNvbW1hbmQ6CiAgICAgICAgICAtIHZsbG0KICAgICAgICAgIC0gc2VydmUKICAgICAgICAgIC0ge3sgLk1vZGVsQXJncy5wYXRoIH19CiAgICAgICAgICAtIC0taG9zdAogICAgICAgICAgLSAiMC4wLjAuMCIKICAgICAgICAgIC0gIi0tcG9ydCIKICAgICAgICAgIC0gIjgwMDAiCiAgICAgICAgICAtIC0tc2VydmVkLW1vZGVsLW5hbWUKICAgICAgICAgIC0ge3sgLk1vZGVsQXJncy5zZXJ2ZV9uYW1lIH19CiAgICAgICAgICAtIC0tdGFzawogICAgICAgICAge3stIGlmIGVxIC5Nb2RlbEFyZ3MudGFzayAidGV4dC1lbWJlZGRpbmciIH19CiAgICAgICAgICAtIGVtYmVkZGluZwogICAgICAgICAge3stIGVsc2UgaWYgZXEgLk1vZGVsQXJncy50YXNrICJ0ZXh0LWdlbmVyYXRpb24iIH19CiAgICAgICAgICAtIGdlbmVyYXRlCiAgICAgICAgICB7ey0gZWxzZSBpZiBlcSAuTW9kZWxBcmdzLnRhc2sgInRleHQtcmVyYW5rIiB9fQogICAgICAgICAgLSByZXJhbmsKICAgICAgICAgIHt7LSBlbHNlIH19CiAgICAgICAgICAtIHt7IC5Nb2RlbEFyZ3MudGFzayB9fQogICAgICAgICAge3stIGVuZCB9fQogICAgICAgICAge3stIGlmIC5FbmdpbmVBcmdzIH19CiAgICAgICAgICB7ey0gcmFuZ2UgJGtleSwgJHZhbHVlIDo9IC5FbmdpbmVBcmdzIH19CiAgICAgICAgICAtIC0te3sgJGtleSB9fQogICAgICB7ey0gaWYgbmUgKHByaW50ZiAiJXYiICR2YWx1ZSkgInRydWUifX0KICAgICAgICAgIC0gInt7ICR2YWx1ZSB9fSIKICAgICAge3stIGVuZCB9fQogICAgICAgICAge3stIGVuZCB9fQogICAgICAgICAge3stIGVuZCB9fQogICAgICAgICAgcmVzb3VyY2VzOgogICAgICAgICAgICBsaW1pdHM6CiAgICAgICAgICAgICAge3stIHJhbmdlICRrZXksICR2YWx1ZSA6PSAuUmVzb3VyY2VzIH19CiAgICAgICAgICAgICAge3sgJGtleSB9fToge3sgJHZhbHVlIH19CiAgICAgICAgICAgICAge3stIGVuZCB9fQogICAgICAgICAgICByZXF1ZXN0czoKICAgICAgICAgICAgICB7ey0gcmFuZ2UgJGtleSwgJHZhbHVlIDo9IC5SZXNvdXJjZXMgfX0KICAgICAgICAgICAgICB7eyAka2V5IH19OiB7eyAkdmFsdWUgfX0KICAgICAgICAgICAgICB7ey0gZW5kIH19CiAgICAgICAgICBlbnY6CiAgICAgICAgICAge3sgcmFuZ2UgJGtleSwgJHZhbHVlIDo9IC5FbnYgfX0KICAgICAgICAgICAtIG5hbWU6IHt7ICRrZXkgfX0KICAgICAgICAgICAgIHZhbHVlOiAie3sgJHZhbHVlIH19IgogICAgICAgICAgIHt7IGVuZCB9fQogICAgICAgICAgcG9ydHM6CiAgICAgICAgICAgIC0gY29udGFpbmVyUG9ydDogODAwMAogICAgICAgICAgc3RhcnR1cFByb2JlOgogICAgICAgICAgICBodHRwR2V0OgogICAgICAgICAgICAgIHBhdGg6IC9oZWFsdGgKICAgICAgICAgICAgICBwb3J0OiA4MDAwCiAgICAgICAgICAgIGluaXRpYWxEZWxheVNlY29uZHM6IDUKICAgICAgICAgICAgdGltZW91dFNlY29uZHM6IDUKICAgICAgICAgICAgcGVyaW9kU2Vjb25kczogMTAKICAgICAgICAgICAgc3VjY2Vzc1RocmVzaG9sZDogMQogICAgICAgICAgICBmYWlsdXJlVGhyZXNob2xkOiAxMjAKICAgICAgICAgIHJlYWRpbmVzc1Byb2JlOgogICAgICAgICAgICBodHRwR2V0OgogICAgICAgICAgICAgIHBhdGg6IC9oZWFsdGgKICAgICAgICAgICAgICBwb3J0OiA4MDAwCiAgICAgICAgICAgIGluaXRpYWxEZWxheVNlY29uZHM6IDUKICAgICAgICAgICAgdGltZW91dFNlY29uZHM6IDUKICAgICAgICAgICAgcGVyaW9kU2Vjb25kczogMTAKICAgICAgICAgICAgc3VjY2Vzc1RocmVzaG9sZDogMQogICAgICAgICAgICBmYWlsdXJlVGhyZXNob2xkOiAzCiAgICAgICAgICB7ey0gaWYgLlZvbHVtZU1vdW50cyB9fQogICAgICAgICAgdm9sdW1lTW91bnRzOgp7eyAuVm9sdW1lTW91bnRzIHwgdG9ZYW1sIHwgaW5kZW50IDEwIH19CiAgICAgICAgICB7ey0gZW5kIH19`

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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := newDeploymentManifestVariables()
			k.setEngineArgs(&data, tt.endpoint, tt.engine)
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
			name: "return Deleting for existing deployment when deletion timestamp is set",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Metadata.DeletionTimestamp = "2024-01-01T00:00:00Z"
				return ep
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).WithDeployment(newEndpoint().Metadata.Name, 1, 1, 1)
			},
			expectedPhase:  v1.EndpointPhaseDELETING,
			expectErrorMsg: "Endpoint deleting in progress",
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
