package plugin

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGPUAcceleratorPluginResolveClusterVirtualizationConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	clusterPolicy := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "nvidia.com/v1",
			"kind":       "ClusterPolicy",
			"metadata": map[string]interface{}{
				"name": "gpu-cluster-policy",
			},
			"spec": map[string]interface{}{
				"driver": map[string]interface{}{
					"enabled": true,
				},
				"devicePlugin": map[string]interface{}{
					"enabled": true,
				},
			},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node",
			Labels: map[string]string{
				NvidiaGPUDiscoveryLabelKey: NvidiaGPUDiscoveryLabelValue,
			},
		},
	}
	ctrlClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(clusterPolicy, node).
		Build()
	originalClientFromCluster := getVirtualizationClientFromCluster
	getVirtualizationClientFromCluster = func(*v1.Cluster) (client.Client, error) {
		return ctrlClient, nil
	}
	t.Cleanup(func() {
		getVirtualizationClientFromCluster = originalClientFromCluster
	})

	config, err := (&GPUAcceleratorPlugin{}).ResolveClusterVirtualizationConfig(context.Background(), &v1.Cluster{})

	require.NoError(t, err)
	require.NotNil(t, config)
	require.True(t, config.Supported)
	require.Equal(t, []string{"gpu-node"}, config.CandidateNodes)
	require.Equal(t, NvidiaGPUOperatorDriverRoot, config.ConfigPatch["devicePlugin"].(map[string]interface{})["nvidiaDriverRoot"])
	require.Contains(t, config.BlockingReasons, "NVIDIA GPU Operator devicePlugin is enabled; disable it before enabling HAMi NVIDIA vGPU")
}
