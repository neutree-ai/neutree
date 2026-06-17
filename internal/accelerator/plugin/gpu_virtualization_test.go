package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNVIDIAGPU_ResolveVirtualizationConfig(t *testing.T) {
	gpuPlugin := &GPUAcceleratorPlugin{}

	config, err := gpuPlugin.ResolveVirtualizationConfig(context.Background(), VirtualizationConfigInput{
		Nodes: []corev1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gpu-label",
					Labels: map[string]string{
						NvidiaGPUDiscoveryLabelKey: NvidiaGPUDiscoveryLabelValue,
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cpu-only",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mig-node",
					Labels: map[string]string{
						NvidiaGPUDiscoveryLabelKey: NvidiaGPUDiscoveryLabelValue,
						"nvidia.com/mig.strategy":  "mixed",
					},
				},
			},
		},
		GPUOperatorClusterPolicies: []GPUOperatorClusterPolicy{
			{
				Name: "cluster-policy",
				Spec: map[string]interface{}{
					"driver": map[string]interface{}{
						"enabled": true,
					},
					"devicePlugin": map[string]interface{}{
						"enabled": true,
					},
				},
			},
		},
	})

	require.NoError(t, err)
	assert.True(t, config.Supported)
	assert.Equal(t, []string{"gpu-label", "mig-node"}, config.CandidateNodes)
	assert.Equal(t, VirtualizationNodeScopeLabel{
		Key:           NvidiaGPUVirtualizationLabelKey,
		EnabledValue:  "true",
		DisabledValue: "false",
	}, config.NodeScopeLabel)
	assert.Equal(t, map[string]interface{}{
		"devicePlugin": map[string]interface{}{
			"nvidiaDriverRoot": NvidiaGPUOperatorDriverRoot,
		},
	}, config.ConfigPatch)
	assert.Contains(t, config.BlockingReasons[0], "NVIDIA GPU Operator devicePlugin is enabled")
}

func TestNVIDIAGPU_ResolveVirtualizationConfigIgnoresGlobalMIGStrategy(t *testing.T) {
	gpuPlugin := &GPUAcceleratorPlugin{}

	config, err := gpuPlugin.ResolveVirtualizationConfig(context.Background(), VirtualizationConfigInput{
		Nodes: []corev1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gpu-node",
					Labels: map[string]string{
						NvidiaGPUDiscoveryLabelKey: NvidiaGPUDiscoveryLabelValue,
					},
				},
			},
		},
		GPUOperatorClusterPolicies: []GPUOperatorClusterPolicy{
			{
				Name: "cluster-policy",
				Spec: map[string]interface{}{
					"mig": map[string]interface{}{
						"strategy": "single",
					},
					"devicePlugin": map[string]interface{}{
						"enabled": false,
					},
				},
			},
		},
	})

	require.NoError(t, err)
	require.True(t, config.Supported)
	assert.Empty(t, config.BlockingReasons)
	assert.Equal(t, []string{"gpu-node"}, config.CandidateNodes)
}
