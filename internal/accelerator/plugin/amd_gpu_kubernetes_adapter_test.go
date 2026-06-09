package plugin

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestAMDGPUResourceParser_KubernetesResourceAdapters(t *testing.T) {
	adapters := (&AMDGPUResourceParser{}).KubernetesResourceAdapters(resourceview.KubernetesResourceAdapterContext{})

	require.Len(t, adapters, 1)
	require.IsType(t, &AMDGPUStandardResourceAdapter{}, adapters[0])
}

func TestAMDGPUStandardResourceAdapter_ParseKubernetesNode(t *testing.T) {
	adapter := &AMDGPUStandardResourceAdapter{}
	input := resourceview.KubernetesNodeResourceContext{
		AllocatableResources: map[corev1.ResourceName]resource.Quantity{
			AMDGPUKubernetesResource: resource.MustParse("4"),
		},
		AvailableResources: map[corev1.ResourceName]resource.Quantity{
			AMDGPUKubernetesResource: resource.MustParse("2"),
		},
		Labels: map[string]string{
			AMDGPUKubernetesNodeSelectorKey: "AMD-MI300X",
		},
	}

	require.True(t, adapter.MatchKubernetesNode(input))
	result, err := adapter.ParseKubernetesNode(input)

	require.NoError(t, err)
	allocatable := result.Allocatable.AcceleratorGroups[v1.AcceleratorTypeAMDGPU]
	require.Equal(t, float64(4), allocatable.Quantity)
	require.Equal(t, float64(4), allocatable.ProductGroups["AMD-MI300X"])

	available := result.Available.AcceleratorGroups[v1.AcceleratorTypeAMDGPU]
	require.Equal(t, float64(2), available.Quantity)
	require.Equal(t, float64(2), available.ProductGroups["AMD-MI300X"])
}
