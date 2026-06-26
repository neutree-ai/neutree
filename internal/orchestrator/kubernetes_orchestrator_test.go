package orchestrator

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/engine"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
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
							Reason:  k8sContainerReasonCrashLoopBackOff,
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
							Reason:  k8sContainerReasonImagePullBackOff,
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
							Reason:  k8sContainerReasonOOMKilled,
							Message: "Out of memory",
						},
					},
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: k8sContainerReasonCrashLoopBackOff,
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

func (f *FakeK8sClient) WithInitContainerInCrashLoopBackOff(containerName string, restartCount int32) *FakeK8sClient {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-init-crash",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat-model",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         containerName,
					Ready:        false,
					RestartCount: restartCount,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  k8sContainerReasonCrashLoopBackOff,
							Message: "Init container is crashing",
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

func (f *FakeK8sClient) WithInitContainerInImagePullBackOff(containerName string) *FakeK8sClient {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-init-image-pull",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat-model",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  containerName,
					Ready: false,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  k8sContainerReasonImagePullBackOff,
							Message: "Failed to pull init container image",
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

func (f *FakeK8sClient) WithInitContainerOOMKilled(containerName string) *FakeK8sClient {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-init-oom",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat-model",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  containerName,
					Ready: false,
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							Reason:  k8sContainerReasonOOMKilled,
							Message: "Out of memory",
						},
					},
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: k8sContainerReasonCrashLoopBackOff,
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

func (f *FakeK8sClient) WithTerminatedInitContainer(containerName string, exitCode int32, restartCount int32) *FakeK8sClient {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-init-terminated",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat-model",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         containerName,
					Ready:        false,
					RestartCount: restartCount,
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: exitCode,
							Reason:   "Error",
							Message:  "Init container terminated with error",
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

func (f *FakeK8sClient) WithRunningInitContainer(containerName string) *FakeK8sClient {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-init-running",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat-model",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  containerName,
					Ready: false,
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{
							StartedAt: metav1.Now(),
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

func (f *FakeK8sClient) WithWaitingInitContainer(containerName, reason string) *FakeK8sClient {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-init-waiting",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat-model",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  containerName,
					Ready: false,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: reason,
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

func TestKubernetesOrchestratorValidateDependenciesForAcceleratorVirtualization(t *testing.T) {
	baseContext := func() *OrchestratorContext {
		return &OrchestratorContext{
			Cluster: &v1.Cluster{
				Metadata: &v1.Metadata{Name: "cluster", Workspace: "workspace"},
				Spec: &v1.ClusterSpec{
					Type: v1.KubernetesClusterType,
				},
				Status: &v1.ClusterStatus{
					Phase:        v1.ClusterPhaseRunning,
					ResourceInfo: validVirtualizationResourceInfo(),
				},
			},
			Engine: &v1.Engine{
				Metadata: &v1.Metadata{Name: "engine", Workspace: "workspace"},
				Status:   &v1.EngineStatus{Phase: v1.EnginePhaseCreated},
			},
			ModelRegistry: &v1.ModelRegistry{
				Metadata: &v1.Metadata{Name: "model-registry", Workspace: "workspace"},
				Status:   &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
			},
			ImageRegistry: &v1.ImageRegistry{
				Metadata: &v1.Metadata{Name: "image-registry", Workspace: "workspace"},
				Status:   &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED},
			},
			Endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "endpoint", Workspace: "workspace"},
				Spec: &v1.EndpointSpec{
					Resources: &v1.ResourceSpec{
						GPU: pointer.String("1"),
						Accelerator: map[string]string{
							v1.AcceleratorTypeKey:                      string(v1.AcceleratorTypeNVIDIAGPU),
							v1.AcceleratorProductKey:                   "NVIDIA_A100",
							v1.AcceleratorVirtualizationMemoryMiBKey:   "1024",
							v1.AcceleratorVirtualizationCorePercentKey: "30",
						},
					},
				},
			},
		}
	}

	t.Run("rejects vGPU endpoint when cluster does not enable accelerator virtualization", func(t *testing.T) {
		err := newKubernetesOrchestrator(Options{}).validateDependencies(baseContext())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "accelerator virtualization is not enabled")
	})

	t.Run("rejects vGPU endpoint when accelerator virtualization component is not ready", func(t *testing.T) {
		ctx := baseContext()
		ctx.Cluster.Spec.AcceleratorVirtualization = &v1.AcceleratorVirtualizationSpec{Enabled: true}
		ctx.Cluster.Status.ComponentStatus = map[string]*v1.ComponentStatus{
			v1.ComponentStatusAcceleratorVirtualizationKey: {
				Phase:   v1.ComponentPhaseNotReady,
				Reason:  "Installing",
				Message: "waiting for device plugin",
			},
		}

		err := newKubernetesOrchestrator(Options{}).validateDependencies(ctx)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "accelerator virtualization component is not ready")
	})

	t.Run("allows vGPU endpoint when accelerator virtualization component is ready", func(t *testing.T) {
		ctx := baseContext()
		ctx.Cluster.Spec.AcceleratorVirtualization = &v1.AcceleratorVirtualizationSpec{Enabled: true}
		ctx.Cluster.Status.ComponentStatus = map[string]*v1.ComponentStatus{
			v1.ComponentStatusAcceleratorVirtualizationKey: {
				Phase: v1.ComponentPhaseReady,
			},
		}

		err := newKubernetesOrchestrator(Options{}).validateDependencies(ctx)

		require.NoError(t, err)
	})

}

func validVirtualizationResourceInfo() *v1.ClusterResources {
	return &v1.ClusterResources{
		ResourceStatus: v1.ResourceStatus{
			Available: &v1.ResourceInfo{
				AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
					v1.AcceleratorTypeNVIDIAGPU: {
						Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
							"NVIDIA_A100": {
								Quantity: 2,
								Virtualization: &v1.AcceleratorVirtualizationResource{
									MemoryMiB: 32768,
									CoreUnits: 200,
								},
							},
						},
					},
				},
			},
		},
		AcceleratorMetadata: map[v1.AcceleratorType]*v1.AcceleratorMetadata{
			v1.AcceleratorTypeNVIDIAGPU: {
				Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata{
					"NVIDIA_A100": {
						MemoryTotalMiB: 40960,
					},
				},
			},
		},
	}
}

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

	objs, err := buildDeploymentObjects(realEmbeddedTemplate(t, "vllm-v0.11.2"), data)
	if err != nil {
		t.Fatalf("Failed to build deployment: %v", err)
	}

	if objs.Items[0].GetName() != "test-endpoint" {
		t.Errorf("Expected deployment name 'test-endpoint', got '%s'", objs.Items[0].GetName())
	}

	// Additional checks can be added here to validate the structure of the generated object
}

func TestBuildVllmDeploymentWithPodAnnotations(t *testing.T) {
	data := DeploymentManifestVariables{
		NeutreeVersion:  "v0.1.0",
		ClusterName:     "test-cluster",
		Workspace:       "test-workspace",
		Namespace:       "default",
		ImagePrefix:     "registry.example.com",
		ImageRepo:       "myrepo",
		ImageTag:        "v1.0.0",
		ImagePullSecret: "my-secret",
		EngineName:      "vllm",
		EngineVersion:   "v0.17.1",
		EndpointName:    "test-endpoint",
		ModelArgs: map[string]interface{}{
			"name":          "gpt-4",
			"task":          "text-generation",
			"path":          "/mnt/models/gpt-4",
			"registry_type": "bentoml",
			"registry_path": "/mnt/registry/gpt-4-model",
			"serve_name":    "gpt-4-serve",
		},
		Resources: map[string]string{
			"nvidia.com/gpu":      "1",
			"nvidia.com/gpumem":   "10240",
			"nvidia.com/gpucores": "30",
		},
		Annotations: map[string]string{
			"neutree.ai/test-annotation": "enabled",
		},
		NodeSelector: map[string]string{
			"nvidia.com/gpu.product": "Tesla-T4",
		},
		RoutingLogic: "roundrobin",
		Replicas:     1,
	}

	objs, err := buildDeploymentObjects(realEmbeddedTemplate(t, "vllm-v0.17.1"), data)
	require.NoError(t, err)
	require.Len(t, objs.Items, 1)

	var deployment appsv1.Deployment
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(objs.Items[0].Object, &deployment))
	assert.Equal(t, "enabled", deployment.Spec.Template.Annotations["neutree.ai/test-annotation"])
	assert.Equal(t, "Tesla-T4", deployment.Spec.Template.Spec.NodeSelector["nvidia.com/gpu.product"])
}

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

	objs, err := buildDeploymentObjects(realEmbeddedTemplate(t, "llama-cpp-v0.3.7"), data)
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

func Test_getDeployTemplate(t *testing.T) {
	k := &kubernetesOrchestrator{}

	// Drive the test through the real embedded vLLM template instead of an
	// inline base64 fixture. This proves the decode path against production
	// data and removes any "test fixture is out of sync with the real
	// template" failure mode.
	vllmTemplateB64, err := engine.GetDeployTemplate("vllm-v0.11.2")
	require.NoError(t, err)
	vllmTemplateRaw := realEmbeddedTemplate(t, "vllm-v0.11.2")

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
									"default": vllmTemplateB64,
								},
							},
						},
					},
				},
			},
			expectedTemplate: vllmTemplateRaw,
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
		{
			name: "sglang engine with GPU > 1 should auto-set tp_size",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "sglang",
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
				"tp_size": 4,
			},
		},
		{
			name: "sglang engine with GPU <= 1 should not set tp_size",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "sglang",
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
			name: "sglang engine fractional GPU should not set tp_size",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "sglang",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Resources: &v1.ResourceSpec{
						GPU: pointer.String("2.5"),
					},
				},
			},
			expectedArgs: map[string]interface{}{},
		},
		{
			name: "sglang engine user-provided kebab tp-size prevents default",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "sglang",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Resources: &v1.ResourceSpec{
						GPU: pointer.String("4"),
					},
					Variables: map[string]interface{}{
						"engine_args": map[string]interface{}{
							"tp-size": "2",
						},
					},
				},
			},
			expectedArgs: map[string]interface{}{
				"tp-size": "2",
			},
		},
		{
			name: "sglang engine user-provided underscore key prevents default",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "sglang",
				},
			},
			endpoint: &v1.Endpoint{
				Spec: &v1.EndpointSpec{
					Resources: &v1.ResourceSpec{
						GPU: pointer.String("4"),
					},
					Variables: map[string]interface{}{
						"engine_args": map[string]interface{}{
							"tp_size": "1",
						},
					},
				},
			},
			expectedArgs: map[string]interface{}{
				"tp_size": "1",
			},
		},
		{
			name: "sglang engine with nil resources should not set tp_size",
			engine: &v1.Engine{
				Metadata: &v1.Metadata{
					Name: "sglang",
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

func TestPrepareEngineArgsForTemplate_VLLMListSemantics(t *testing.T) {
	args := map[string]interface{}{
		"served_model_name":          []interface{}{"base-model", "neu-vllm-list-alias"},
		"logits_processors":          `["a.Processor","b.Processor"]`,
		"override_generation_config": map[string]interface{}{"temperature": 0.8},
		"gpu_memory_utilization":     "0.85",
		"enable_prefix_caching":      "false",
	}

	prepareEngineArgsForTemplate(args, v1.EngineNameVLLM)

	assert.Equal(t, []interface{}{"base-model", "neu-vllm-list-alias"}, args["served_model_name"])
	assert.Equal(t, []interface{}{"a.Processor", "b.Processor"}, args["logits_processors"])
	assert.Equal(t, `{\"temperature\":0.8}`, args["override_generation_config"])
	assert.Equal(t, "0.85", args["gpu_memory_utilization"])
	assert.Equal(t, "false", args["enable_prefix_caching"])
}

func TestPrepareEngineArgsForTemplate_NonVLLMListCompatibility(t *testing.T) {
	args := map[string]interface{}{
		"cuda_graph_bs": []interface{}{1, 2},
	}

	prepareEngineArgsForTemplate(args, v1.EngineNameSGLang)

	assert.Equal(t, `[1,2]`, args["cuda_graph_bs"])
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
			// Paused-from-start (applied with replicas=0): no deployment is
			// ever created, so observed state should be Paused, not
			// Deploying("not found"). The Paused check therefore runs before
			// the !exists check.
			name: "return Paused for paused endpoint when deployment does not exist",
			inputEndpoint: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Spec.Replicas.Num = pointer.Int(0)
				return ep
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).WithDeploymentNotFound()
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
			expectErrorMsg: k8sContainerReasonCrashLoopBackOff,
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
			name: "return Failed for init container in CrashLoopBackOff with high restart count",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithInitContainerInCrashLoopBackOff(modelDownloaderInitContainerName, 5)
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "Init Container",
			expectError:    false,
		},
		{
			name: "return Failed for init container with ImagePullBackOff",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithInitContainerInImagePullBackOff(modelDownloaderInitContainerName)
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "Init Container",
			expectError:    false,
		},
		{
			name: "return Failed for init container with OOMKilled",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithInitContainerOOMKilled(modelDownloaderInitContainerName)
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "Init Container",
			expectError:    false,
		},
		{
			name: "return ModelDownloading for terminated model-downloader init container below retry threshold",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithTerminatedInitContainer(modelDownloaderInitContainerName, 1, 1)
			},
			expectedPhase:  v1.EndpointPhaseMODELDOWNLOADING,
			expectErrorMsg: modelDownloaderInitContainerName + " init container has not completed",
			expectError:    false,
		},
		{
			name: "return Failed for terminated model-downloader init container after retry threshold",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithTerminatedInitContainer(modelDownloaderInitContainerName, 1, 5)
			},
			expectedPhase:  v1.EndpointPhaseFAILED,
			expectErrorMsg: "exit code 1",
			expectError:    false,
		},
		{
			name: "return ModelDownloading while model-downloader init container is running",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithRunningInitContainer(modelDownloaderInitContainerName)
			},
			expectedPhase:  v1.EndpointPhaseMODELDOWNLOADING,
			expectErrorMsg: modelDownloaderInitContainerName + " init container is running",
			expectError:    false,
		},
		{
			name: "return ModelDownloading while model-downloader init container is waiting",
			inputEndpoint: func() *v1.Endpoint {
				return newEndpoint()
			},
			setupMock: func(t *testing.T) *FakeK8sClient {
				return NewFakeK8sClient(t).
					WithDeployment(newEndpoint().Metadata.Name, 1, 0, 0).
					WithWaitingInitContainer(modelDownloaderInitContainerName, k8sContainerReasonPodInitializing)
			},
			expectedPhase:  v1.EndpointPhaseMODELDOWNLOADING,
			expectErrorMsg: modelDownloaderInitContainerName + " init container has not completed",
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

			status, err := o.getEndpointStats(fakeClient, "test-namespace", &v1.Cluster{Spec: &v1.ClusterSpec{}}, tt.inputEndpoint())

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

func TestKubernetesOrchestrator_getEndpointStatsIgnoresResourceStatusError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	endpoint := &v1.Endpoint{
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

	fakeClient := NewFakeK8sClient(t).
		WithDeployment(endpoint.Metadata.Name, 1, 1, 1)

	require.NoError(t, fakeClient.Create(context.Background(), &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node",
			Labels: map[string]string{
				plugin.NvidiaGPUVirtualizationLabelKey:    "true",
				plugin.NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
			},
			Annotations: map[string]string{
				plugin.HAMiNodeNvidiaRegisterAnnotation: `[{"id":"GPU-1","devmem":15360,"devcore":100,"type":"NVIDIA-Tesla T4","health":true}]`,
			},
		},
	}))
	require.NoError(t, fakeClient.Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat-model-0",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": endpoint.Metadata.Name,
			},
			Annotations: map[string]string{
				plugin.HAMiVGPUDevicesAllocatedAnnotation: ";GPU-1,NVIDIA,invalid-memory,100:;",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "gpu-node",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}))

	o := &kubernetesOrchestrator{
		acceleratorMgr: accelerator.NewManager(gin.New()),
	}
	cluster := &v1.Cluster{
		Spec: &v1.ClusterSpec{
			AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
				Enabled: true,
			},
		},
	}

	status, err := o.getEndpointStats(fakeClient, "test-namespace", cluster, endpoint)

	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, v1.EndpointPhaseRUNNING, status.Phase)
	assert.Nil(t, status.Resources)
}

// TestBuildDeployment_BooleanEngineArgs pins the boolean handling in the
// K8s deploy templates that ship engine_args through to vLLM / SGLang CLI
// argparse. Both engines' boolean flags are registered with
// action="store_true": they accept `--flag` (no value follows) and reject
// `--flag false` (nargs=0). The template contract is therefore:
//
//   - value "true"  (bool or string) -> emit "--<flag>" only
//   - value "false" (bool or string) -> emit nothing (engine takes its default)
//   - anything else                   -> emit "--<flag>" followed by "<value>"
//
// The test renders the embedded production templates (not an inline fixture),
// passes a matrix of bool/string true/false alongside non-boolean values, and
// asserts the rendered CLI tokens never contain a literal "false" token.
func TestBuildDeployment_BooleanEngineArgs(t *testing.T) {
	cases := []struct {
		name        string
		templateKey string
	}{
		{
			name:        "vllm-v0.11.2",
			templateKey: "vllm-v0.11.2",
		},
		{
			name:        "vllm-v0.17.1",
			templateKey: "vllm-v0.17.1",
		},
		{
			name:        "sglang-v0.5.10",
			templateKey: "sglang-v0.5.10",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := realEmbeddedTemplate(t, tc.templateKey)

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
					// Boolean false in both bool and string form, kebab and
					// underscore key shape: the skip branch must drop the flag
					// regardless of how the key was spelled.
					"enable_prefix_caching": false,
					"skip-tokenizer-init":   "false",
					// Boolean true in both forms: emit flag, no following value.
					// Keys are kebab-form so the SGLang template's
					// `replace "_" "-"` step is a no-op here — `replace` is
					// evaluated in the true/non-bool branches but not the skip
					// branch, so true booleans are the right place to assert
					// the rendered flag name matches across engines.
					"trust-remote-code": true,
					"is-embedding":      "true",
					// Non-boolean values: emit flag followed by quoted value.
					"max-model-len": "4096",
					"max-num-seqs":  256,
				},
				Resources: map[string]string{
					"cpu":    "500m",
					"memory": "1Gi",
				},
				RoutingLogic: "roundrobin",
				Replicas:     1,
			}

			objs, err := buildDeploymentObjects(tmpl, data)
			require.NoError(t, err, "template render must succeed")

			tokens := extractEngineCLITokens(t, objs)

			// (1) False booleans must be skipped entirely — no flag, no value.
			//     Check both kebab and underscore forms because SGLang transforms
			//     `_` to `-` at render time while vLLM passes the key through.
			assert.NotContains(t, tokens, "--enable-prefix-caching",
				"bool false engine_arg must not emit its flag (kebab form)")
			assert.NotContains(t, tokens, "--enable_prefix_caching",
				"bool false engine_arg must not emit its flag (underscore form)")
			assert.NotContains(t, tokens, "--skip-tokenizer-init",
				"string false engine_arg must not emit its flag")
			// The literal token `false` must never reach the CLI: argparse rejects
			// `--flag false` for store_true booleans.
			for _, tok := range tokens {
				assert.NotEqual(t, "false", tok,
					"no CLI token should be the literal string \"false\"")
			}

			// (2) True booleans (bool + string forms) must emit just the flag.
			//     The token immediately after must not be "true" — it should be
			//     the next engine_arg flag or a CLI token following it.
			assertFlagWithoutValue(t, tokens, "--trust-remote-code")
			assertFlagWithoutValue(t, tokens, "--is-embedding")

			// (3) Non-boolean engine_args must emit `--<flag>` followed by value.
			assertFlagWithValue(t, tokens, "--max-model-len", "4096")
			assertFlagWithValue(t, tokens, "--max-num-seqs", "256")
		})
	}
}

func TestBuildDeployment_VLLMListEngineArgs(t *testing.T) {
	cases := []struct {
		name        string
		templateKey string
	}{
		{
			name:        "vllm-v0.11.2",
			templateKey: "vllm-v0.11.2",
		},
		{
			name:        "vllm-v0.17.1",
			templateKey: "vllm-v0.17.1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := realEmbeddedTemplate(t, tc.templateKey)

			data := DeploymentManifestVariables{
				NeutreeVersion:  "v0.1.0",
				ClusterName:     "test-cluster",
				Workspace:       "test-workspace",
				Namespace:       "default",
				ImagePrefix:     "registry.example.com",
				ImageRepo:       "myrepo",
				ImageTag:        "v1.0.0",
				ImagePullSecret: "my-secret",
				EngineName:      v1.EngineNameVLLM,
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
					"served_model_name": []interface{}{"gpt-4", "neu-vllm-list-alias"},
					"logits_processors": []interface{}{"a.Processor", "b.Processor"},
				},
				Resources: map[string]string{
					"cpu":    "500m",
					"memory": "1Gi",
				},
				RoutingLogic: "roundrobin",
				Replicas:     1,
			}

			objs, err := buildDeploymentObjects(tmpl, data)
			require.NoError(t, err, "template render must succeed")

			tokens := extractEngineCLITokens(t, objs)

			assertFlagWithValues(t, tokens, "--served_model_name", "gpt-4", "neu-vllm-list-alias")
			assertFlagWithValues(t, tokens, "--logits_processors", "a.Processor", "b.Processor")
			assert.NotContains(t, tokens, `["gpt-4","neu-vllm-list-alias"]`)
			assert.NotContains(t, tokens, `["a.Processor","b.Processor"]`)
		})
	}
}

// makePauseTestCtx builds a minimal OrchestratorContext for pause/delete tests:
// only fields that pauseEndpoint / deleteEndpoint actually read.
func makePauseTestCtx(ctrlClient client.Client, name string) *OrchestratorContext {
	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Name: "test-cluster"},
		Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType},
		Status:   &v1.ClusterStatus{Phase: v1.ClusterPhaseRunning},
	}
	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{Name: name, Workspace: "default"},
		Spec: &v1.EndpointSpec{
			Cluster:  "test-cluster",
			Replicas: v1.ReplicaSpec{Num: pointer.Int(0)},
		},
	}
	return &OrchestratorContext{
		Cluster:   cluster,
		Endpoint:  endpoint,
		ctrClient: ctrlClient,
		logger:    klogTestLogger(),
	}
}

// createTestDeployment seeds a Deployment in the namespace pauseEndpoint /
// deleteEndpoint will look at — i.e. util.ClusterNamespace(ctx.Cluster) — so
// the test does not depend on FakeK8sClient.WithDeployment's hard-coded
// "test-namespace".
func createTestDeployment(t *testing.T, fakeClient *FakeK8sClient, ctx *OrchestratorContext, name string, replicas int32) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: util.ClusterNamespace(ctx.Cluster),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"endpoint": name}},
		},
	}
	require.NoError(t, fakeClient.Create(context.Background(), dep))
}

func TestKubernetesOrchestrator_pauseEndpoint(t *testing.T) {
	const name = "chat-model"

	tests := []struct {
		name             string
		seedReplicas     *int32 // nil => no deployment
		expectError      bool
		expectedReplicas *int32
	}{
		{
			name:             "patches replicas to 0 when deployment exists with replicas=1",
			seedReplicas:     int32Ptr(1),
			expectError:      false,
			expectedReplicas: int32Ptr(0),
		},
		{
			name:             "no-op when deployment is already at replicas=0",
			seedReplicas:     int32Ptr(0),
			expectError:      false,
			expectedReplicas: int32Ptr(0),
		},
		{
			name:             "no-op when deployment does not exist",
			seedReplicas:     nil,
			expectError:      false,
			expectedReplicas: nil, // no deployment to inspect
		},
		{
			// pauseEndpoint must not touch storage / model registry — only K8s API.
			name:             "succeeds without any storage interaction",
			seedReplicas:     int32Ptr(2),
			expectError:      false,
			expectedReplicas: int32Ptr(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := NewFakeK8sClient(t)
			ctx := makePauseTestCtx(fakeClient, name)
			if tt.seedReplicas != nil {
				createTestDeployment(t, fakeClient, ctx, name, *tt.seedReplicas)
			}

			o := &kubernetesOrchestrator{}
			err := o.pauseEndpoint(ctx)
			if tt.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tt.expectedReplicas == nil {
				return
			}

			dep := &appsv1.Deployment{}
			getErr := fakeClient.Get(context.Background(),
				client.ObjectKey{Namespace: util.ClusterNamespace(ctx.Cluster), Name: name},
				dep)
			require.NoError(t, getErr)
			require.NotNil(t, dep.Spec.Replicas)
			assert.Equal(t, *tt.expectedReplicas, *dep.Spec.Replicas)
		})
	}
}

// realEmbeddedTemplate decodes the production embedded K8s deploy template for
// the given engine key (e.g. "vllm-v0.11.2", "llama-cpp-v0.3.7",
// "sglang-v0.5.10"). Tests that need a representative template should use this
// instead of pasting an inline fixture, so future edits to the real template
// (probe paths, label additions, etc.) propagate automatically and there is no
// "test fixture is out of sync" failure mode.
func realEmbeddedTemplate(t *testing.T, engineKey string) string {
	t.Helper()

	b64, err := engine.GetDeployTemplate(engineKey)
	require.NoError(t, err, "engine.GetDeployTemplate(%q) failed", engineKey)

	raw, err := base64.StdEncoding.DecodeString(b64)
	require.NoError(t, err, "base64 decode of %q template failed", engineKey)

	return string(raw)
}

// extractEngineCLITokens collects the rendered Deployment's first container
// command + args into a flat ordered slice of tokens, so test assertions can
// reason about CLI shape (token presence, adjacency).
func extractEngineCLITokens(t *testing.T, objs *unstructured.UnstructuredList) []string {
	t.Helper()
	require.NotNil(t, objs)
	require.NotEmpty(t, objs.Items, "expected at least one rendered object")

	var dep *unstructured.Unstructured

	for i := range objs.Items {
		if objs.Items[i].GetKind() == "Deployment" {
			dep = &objs.Items[i]
			break
		}
	}

	require.NotNil(t, dep, "rendered manifest must include a Deployment")

	containers, found, err := unstructured.NestedSlice(dep.Object,
		"spec", "template", "spec", "containers")
	require.NoError(t, err)
	require.True(t, found, "deployment must declare containers")
	require.NotEmpty(t, containers, "containers slice must not be empty")

	// Each engine template defines the inference container after any
	// initContainers; we want the first containers[] entry that has the
	// engine binary in command/args. The first containers[] entry in the
	// vLLM / SGLang / llama-cpp templates is exactly that container.
	cMap, ok := containers[0].(map[string]interface{})
	require.True(t, ok)

	var tokens []string

	if cmd, found, _ := unstructured.NestedSlice(cMap, "command"); found {
		tokens = append(tokens, sliceToStrings(t, cmd)...)
	}

	if args, found, _ := unstructured.NestedSlice(cMap, "args"); found {
		tokens = append(tokens, sliceToStrings(t, args)...)
	}

	return tokens
}

func sliceToStrings(t *testing.T, in []interface{}) []string {
	t.Helper()

	out := make([]string, 0, len(in))

	for _, v := range in {
		s, ok := v.(string)
		require.True(t, ok, "expected string CLI token, got %T (%v)", v, v)
		out = append(out, s)
	}

	return out
}

// assertFlagWithoutValue checks that `flag` appears in tokens and the next
// token is either another `--flag` or absent — i.e. no value follows.
//
// The "absent" case (flag is the last token in the slice) is fine: a flag
// at the end of the CLI argv with nothing after it is, by definition, a
// flag-only invocation. require.NotEqual(t, -1, idx) above already ensures
// the flag is present; the early return below is the "no following token to
// check" branch, not a silent skip.
func assertFlagWithoutValue(t *testing.T, tokens []string, flag string) {
	t.Helper()

	idx := indexOf(tokens, flag)
	require.NotEqual(t, -1, idx, "expected flag %q in tokens %v", flag, tokens)

	if idx+1 >= len(tokens) {
		return
	}

	next := tokens[idx+1]
	assert.True(t, strings.HasPrefix(next, "--"),
		"flag %q should not be followed by a value; got %q", flag, next)
}

// assertFlagWithValue checks that `flag` is immediately followed by `value`.
func assertFlagWithValue(t *testing.T, tokens []string, flag, value string) {
	t.Helper()

	idx := indexOf(tokens, flag)
	require.NotEqual(t, -1, idx, "expected flag %q in tokens %v", flag, tokens)
	require.Less(t, idx+1, len(tokens), "flag %q should have a following value token", flag)
	assert.Equal(t, value, tokens[idx+1],
		"flag %q should be followed by %q; got %q", flag, value, tokens[idx+1])
}

func assertFlagWithValues(t *testing.T, tokens []string, flag string, values ...string) {
	t.Helper()

	idx := indexOf(tokens, flag)
	require.NotEqual(t, -1, idx, "expected flag %q in tokens %v", flag, tokens)
	require.LessOrEqual(t, idx+len(values), len(tokens)-1,
		"flag %q should have %d following value token(s)", flag, len(values))

	for i, value := range values {
		assert.Equal(t, value, tokens[idx+1+i],
			"flag %q value token %d should be %q; got %q", flag, i, value, tokens[idx+1+i])
	}
}

func indexOf(tokens []string, target string) int {
	for i, t := range tokens {
		if t == target {
			return i
		}
	}

	return -1
}

// TestKubernetesOrchestrator_deleteEndpoint_NoConfigStore verifies that the
// delete path does not depend on ModelRegistry/Engine/ImageRegistry; it
// relies only on the deployer's ConfigMap-backed last-applied snapshot, so
// DeleteEndpoint succeeds even when the model registry has been removed.
func TestKubernetesOrchestrator_deleteEndpoint_NoConfigStore(t *testing.T) {
	fakeClient := NewFakeK8sClient(t)
	ctx := makePauseTestCtx(fakeClient, "chat-model")

	o := &kubernetesOrchestrator{}
	// With no last-applied ConfigMap and no deployment, delete is a no-op
	// (idempotent). It must not return error and must not require ModelRegistry.
	require.NoError(t, o.deleteEndpoint(ctx))
}

func int32Ptr(v int32) *int32 { return &v }

// klogTestLogger returns the default klog logger. This matches the same
// idiom production code uses (klog.Background()); it is NOT silenced —
// klog still emits according to its global configuration. We rely on the
// default klog config for tests being quiet enough; replace with a discard
// logger if test output ever becomes noisy.
func klogTestLogger() klog.Logger {
	return klog.Background()
}
