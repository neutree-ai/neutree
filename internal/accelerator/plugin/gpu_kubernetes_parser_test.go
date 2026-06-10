package plugin

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestGPUResourceParser_ParseFromKubernetesStandardResources(t *testing.T) {
	parser := &GPUResourceParser{}
	resources := map[corev1.ResourceName]resource.Quantity{
		NvidiaGPUKubernetesResource: resource.MustParse("2"),
	}
	labels := map[string]string{
		NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA_A100",
		NvidiaGPUMemoryNodeLabelKey:        "81920",
	}

	result, err := parser.ParseFromKubernetes(resources, labels)

	require.NoError(t, err)
	group := result.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(2), group.Quantity)
	require.Equal(t, float64(2), group.ProductGroups["NVIDIA_A100"])
	require.Equal(t, float64(2), group.Products["NVIDIA_A100"].Quantity)
	require.Equal(t, float64(81920),
		result.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_A100"].MemoryTotalMiB)
	require.Nil(t, group.Products["NVIDIA_A100"].Virtualization)
}

func TestGPUResourceParser_ParseFromKubernetesDoesNotAddVirtualizationDetails(t *testing.T) {
	parser := &GPUResourceParser{}
	resources := map[corev1.ResourceName]resource.Quantity{
		NvidiaGPUKubernetesResource: resource.MustParse("20"),
		NvidiaGPUMemoryResource:     resource.MustParse("30720"),
		NvidiaGPUCoreResource:       resource.MustParse("200"),
	}
	labels := map[string]string{
		NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
		NvidiaGPUVirtualizationLabelKey:    "true",
		NvidiaGPUCountResource:             "2",
	}

	result, err := parser.ParseFromKubernetes(resources, labels)

	require.NoError(t, err)
	group := result.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(20), group.Quantity)
	require.Equal(t, float64(20), group.ProductGroups["Tesla-T4"])
	require.Nil(t, group.Products)
}
