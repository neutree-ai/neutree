package orchestrator

import (
	"context"
	"encoding/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/kvcache"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCacheDependencyWaitsForOldPods(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "ns"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "ns", Labels: map[string]string{"app": "inference", "endpoint": "test", kvcache.ZCacheDependencyLabel: "true"}}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(dep, pod).Build()
	e := &v1.Endpoint{Metadata: &v1.Metadata{Name: "test"}, Spec: &v1.EndpointSpec{KVCache: &v1.EndpointKVCacheSpec{Provider: "vllm-native"}}, Status: &v1.EndpointStatus{ZCache: &v1.EndpointZCacheStatus{Generation: 4, InUse: true}}}
	observed, err := observeEndpointCache(c, "ns", e)
	require.NoError(t, err)
	require.True(t, observed.InUse)
	require.EqualValues(t, 4, observed.Generation)
	require.NoError(t, c.Delete(context.Background(), pod))
	observed, err = observeEndpointCache(c, "ns", e)
	require.NoError(t, err)
	require.False(t, observed.InUse)

	zero := int32(0)
	dep.Spec.Replicas = &zero
	dep.Spec.Template.Labels = map[string]string{kvcache.ZCacheDependencyLabel: "true"}
	require.NoError(t, c.Update(context.Background(), dep))
	observed, err = observeEndpointCache(c, "ns", e)
	require.NoError(t, err)
	require.False(t, observed.InUse, "a paused old template with no Pods no longer uses cache")
}

func TestNativeCacheDoesNotClaimZCache(t *testing.T) {
	memory := "4"
	e := &v1.Endpoint{Spec: &v1.EndpointSpec{Resources: &v1.ResourceSpec{Memory: &memory}, Engine: &v1.EndpointEngineSpec{Engine: "vllm"}, KVCache: &v1.EndpointKVCacheSpec{Provider: "vllm-native", Options: json.RawMessage(`{"cpu_memory_gib":0.25}`)}}}
	dep := &appsv1.Deployment{TypeMeta: metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"}}
	dep.Spec.Template.Spec.Containers = []corev1.Container{{Name: "vllm", Image: "original"}}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(dep)
	require.NoError(t, err)
	objects := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{{Object: obj}}}
	k := &kubernetesOrchestrator{}
	require.NoError(t, k.configureEndpointCache(&OrchestratorContext{Endpoint: e, Cluster: &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}}}, objects))
	var result appsv1.Deployment
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(objects.Items[0].Object, &result))
	require.Empty(t, result.Spec.Template.Labels[kvcache.ZCacheDependencyLabel])
	require.Nil(t, result.Spec.Template.Spec.Affinity)
	require.Contains(t, result.Spec.Template.Spec.Containers[0].Args, "--kv-offloading-size")
}
