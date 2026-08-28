package allocation

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestKubernetesAllocationProviderBuildsRawAcceleratorEvidence(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	endpointPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "chat-pod",
			UID:         "pod-uid",
			Labels:      map[string]string{"app": endpointPodAppLabelValue, "endpoint": "chat"},
			Annotations: map[string]string{"vendor.example/devices": "raw"},
		},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
	provider := KubernetesAllocationProvider{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			endpointPod,
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:        "node-a",
				Labels:      map[string]string{"vendor.example/model": "raw-model"},
				Annotations: map[string]string{"vendor.example/metadata": "raw"},
			}},
		).WithIndex(&corev1.Pod{}, "spec.nodeName", podNodeNameIndex).Build(),
		NodeName: "node-a",
		PodResources: PodResourceListerFunc(func(context.Context) ([]adapter.PodResource, error) {
			return []adapter.PodResource{{
				Namespace: "default",
				Name:      "chat-pod",
				Containers: []adapter.ContainerDevices{{
					ResourceName: "vendor.example/accelerator",
					DeviceIDs:    []string{"device-0"},
				}},
			}}, nil
		}),
	}

	evidence, err := provider.KubernetesAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.True(t, evidence.AllocationAvailable)
	require.Len(t, evidence.PodResources, 1)
	assert.Equal(t, "vendor.example/accelerator", evidence.PodResources[0].Containers[0].ResourceName)
	require.Len(t, evidence.EndpointPods, 1)
	assert.Equal(t, "pod-uid", evidence.EndpointPods[0].UID)
	assert.Equal(t, "raw", evidence.EndpointPods[0].Annotations["vendor.example/devices"])
	assert.Equal(t, "raw-model", evidence.NodeLabels["vendor.example/model"])
	assert.Equal(t, "raw", evidence.NodeAnnotations["vendor.example/metadata"])
}

func TestKubernetesAllocationProviderPropagatesPodResourceErrors(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	provider := KubernetesAllocationProvider{
		Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
		NodeName: "node-a",
		PodResources: PodResourceListerFunc(func(context.Context) ([]adapter.PodResource, error) {
			return nil, errors.New("pod resources unavailable")
		}),
	}

	evidence, err := provider.KubernetesAcceleratorEvidence(context.Background())

	require.Error(t, err)
	assert.Equal(t, adapter.KubernetesEvidence{}, evidence)
}

func TestKubernetesAllocationProviderWithoutDependenciesReturnsEmptyEvidence(t *testing.T) {
	evidence, err := (KubernetesAllocationProvider{}).KubernetesAcceleratorEvidence(context.Background())

	require.NoError(t, err)
	assert.Equal(t, adapter.KubernetesEvidence{}, evidence)
}

func TestKubernetesAllocationProviderFiltersNonLocalAndTerminalPods(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	pods := []client.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "remote", Labels: map[string]string{"app": endpointPodAppLabelValue, "endpoint": "chat"}}, Spec: corev1.PodSpec{NodeName: "node-b"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "terminal", Labels: map[string]string{"app": endpointPodAppLabelValue, "endpoint": "chat"}}, Spec: corev1.PodSpec{NodeName: "node-a"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "missing-endpoint", Labels: map[string]string{"app": endpointPodAppLabelValue}}, Spec: corev1.PodSpec{NodeName: "node-a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "chat-b", Labels: map[string]string{"app": endpointPodAppLabelValue, "endpoint": "chat"}}, Spec: corev1.PodSpec{NodeName: "node-a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "chat-a", Labels: map[string]string{"app": endpointPodAppLabelValue, "endpoint": "chat"}}, Spec: corev1.PodSpec{NodeName: "node-a"}},
	}
	provider := KubernetesAllocationProvider{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pods...).WithIndex(
			&corev1.Pod{}, "spec.nodeName", podNodeNameIndex,
		).Build(),
		NodeName: "node-a",
	}

	result, err := provider.localEndpointPods(context.Background())

	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, []string{"chat-a", "chat-b"}, []string{result[0].Name, result[1].Name})
}

func podNodeNameIndex(object client.Object) []string {
	pod, ok := object.(*corev1.Pod)
	if !ok || pod.Spec.NodeName == "" {
		return nil
	}

	return []string{pod.Spec.NodeName}
}
