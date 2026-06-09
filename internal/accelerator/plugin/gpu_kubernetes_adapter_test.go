package plugin

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestGPUResourceParser_KubernetesResourceAdapters(t *testing.T) {
	standardAdapters := (&GPUResourceParser{}).KubernetesResourceAdapters(resourceview.KubernetesResourceAdapterContext{})

	require.Len(t, standardAdapters, 1)
	require.IsType(t, &GPUStandardResourceAdapter{}, standardAdapters[0])

	virtualizationAdapters := (&GPUResourceParser{}).KubernetesResourceAdapters(resourceview.KubernetesResourceAdapterContext{
		AcceleratorVirtualizationEnabled: true,
	})

	require.Len(t, virtualizationAdapters, 1)
	require.IsType(t, &GPUVirtualizationResourceAdapter{}, virtualizationAdapters[0])
}

func TestGPUStandardResourceAdapter_ParseKubernetesNode(t *testing.T) {
	adapter := &GPUStandardResourceAdapter{}
	input := resourceview.KubernetesNodeResourceContext{
		AllocatableResources: map[corev1.ResourceName]resource.Quantity{
			NvidiaGPUKubernetesResource: resource.MustParse("2"),
		},
		AvailableResources: map[corev1.ResourceName]resource.Quantity{
			NvidiaGPUKubernetesResource: resource.MustParse("1"),
		},
		Labels: map[string]string{
			NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA_A100",
			NvidiaGPUMemoryNodeLabelKey:        "81920",
		},
	}

	require.True(t, adapter.MatchKubernetesNode(input))
	result, err := adapter.ParseKubernetesNode(input)

	require.NoError(t, err)
	allocatable := result.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(2), allocatable.Quantity)
	require.Equal(t, float64(2), allocatable.ProductGroups["NVIDIA_A100"])
	require.Equal(t, float64(2), allocatable.Products["NVIDIA_A100"].Quantity)
	require.Equal(t, float64(81920),
		result.Allocatable.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_A100"].MemoryTotalMiB)

	available := result.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(1), available.Quantity)
	require.Equal(t, float64(1), available.ProductGroups["NVIDIA_A100"])
	require.Equal(t, float64(1), available.Products["NVIDIA_A100"].Quantity)
}

func TestGPUStandardResourceAdapter_DoesNotMatchVirtualizedNode(t *testing.T) {
	adapter := &GPUStandardResourceAdapter{}

	require.False(t, adapter.MatchKubernetesNode(resourceview.KubernetesNodeResourceContext{
		AllocatableResources: map[corev1.ResourceName]resource.Quantity{
			NvidiaGPUKubernetesResource: resource.MustParse("20"),
		},
		Labels: map[string]string{
			NvidiaGPUVirtualizationLabelKey: "true",
		},
	}))
}

func TestGPUVirtualizationResourceAdapter_UsesStandardAdapterForNonVirtualizedNode(t *testing.T) {
	adapter := &GPUVirtualizationResourceAdapter{}
	input := resourceview.KubernetesNodeResourceContext{
		AllocatableResources: map[corev1.ResourceName]resource.Quantity{
			NvidiaGPUKubernetesResource: resource.MustParse("2"),
		},
		AvailableResources: map[corev1.ResourceName]resource.Quantity{
			NvidiaGPUKubernetesResource: resource.MustParse("1"),
		},
		Labels: map[string]string{
			NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA_A100",
			NvidiaGPUMemoryNodeLabelKey:        "81920",
		},
	}

	require.True(t, adapter.MatchKubernetesNode(input))
	result, err := adapter.ParseKubernetesNode(input)

	require.NoError(t, err)
	allocatable := result.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(2), allocatable.Quantity)
	require.Equal(t, float64(2), allocatable.ProductGroups["NVIDIA_A100"])
	require.Nil(t, allocatable.Products["NVIDIA_A100"].Virtualization)
}
