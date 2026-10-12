package kvcache

import (
	"encoding/json"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"testing"
)

func TestNativeConfiguration(t *testing.T) {
	memory := "4"
	endpoint := &v1.Endpoint{Spec: &v1.EndpointSpec{
		Resources: &v1.ResourceSpec{Memory: &memory},
		Engine:    &v1.EndpointEngineSpec{Engine: "vllm"},
		KVCache:   &v1.EndpointKVCacheSpec{Provider: "vllm-native", Options: json.RawMessage(`{"cpu_memory_gib":0.25}`)},
	}}
	cluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}}
	dep := &appsv1.Deployment{}
	dep.Spec.Template.Spec.Containers = []corev1.Container{{Name: "vllm", Image: "original-image", Args: []string{"--model", "test"}}}
	dep.Spec.Template.Spec.NodeSelector = map[string]string{"gpu": "t4"}
	require.NoError(t, Validate(endpoint, cluster, nil))
	require.NoError(t, (Native{}).Configure(endpoint, cluster, dep))
	require.Equal(t, "original-image", dep.Spec.Template.Spec.Containers[0].Image)
	require.Equal(t, []string{"--model", "test", "--kv-offloading-backend", "native", "--kv-offloading-size", "0.25"}, dep.Spec.Template.Spec.Containers[0].Args)
	require.Equal(t, map[string]string{"gpu": "t4"}, dep.Spec.Template.Spec.NodeSelector)
	require.Nil(t, dep.Spec.Template.Spec.Affinity)
	require.Empty(t, dep.Spec.Template.Labels)
	for _, options := range []string{`{"cpu_memory_gib":0}`, `{"cpu_memory_gib":4}`, `{"cpu_memory_gib":0.25,"timeout_seconds":2}`} {
		endpoint.Spec.KVCache.Options = json.RawMessage(options)
		require.Error(t, Validate(endpoint, cluster, nil))
	}
	endpoint.Spec.KVCache.Provider = "zcache"
	require.ErrorContains(t, Validate(endpoint, cluster, nil), "not supported")
	endpoint.Spec.KVCache = nil
	require.NoError(t, Validate(endpoint, cluster, nil))
}
