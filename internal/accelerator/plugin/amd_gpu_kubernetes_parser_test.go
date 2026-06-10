package plugin

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestAMDGPUResourceParser_ParseFromKubernetesStandardResources(t *testing.T) {
	parser := &AMDGPUResourceParser{}
	resources := map[corev1.ResourceName]resource.Quantity{
		AMDGPUKubernetesResource: resource.MustParse("4"),
	}
	labels := map[string]string{
		AMDGPUKubernetesNodeSelectorKey: "AMD-MI300X",
	}

	result, err := parser.ParseFromKubernetes(resources, labels)

	require.NoError(t, err)
	group := result.AcceleratorGroups[v1.AcceleratorTypeAMDGPU]
	require.Equal(t, float64(4), group.Quantity)
	require.Equal(t, float64(4), group.ProductGroups["AMD-MI300X"])
}
