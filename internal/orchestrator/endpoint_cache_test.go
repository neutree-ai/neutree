package orchestrator

import (
	"encoding/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestNativeCacheOnlyConfiguresEngine(t *testing.T) {
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
	require.Empty(t, result.Spec.Template.Labels)
	require.Nil(t, result.Spec.Template.Spec.Affinity)
	require.Contains(t, result.Spec.Template.Spec.Containers[0].Args, "--kv-offloading-size")
}
