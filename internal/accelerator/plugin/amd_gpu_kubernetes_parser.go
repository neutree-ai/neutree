package plugin

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func parseStandardAMDKubernetesResources(
	resources map[corev1.ResourceName]resource.Quantity,
	labels map[string]string,
) (*v1.ResourceInfo, error) {
	if resources == nil || labels == nil {
		return nil, fmt.Errorf("resource or label is nil")
	}

	gpuQuantity, hasGPU := resources[AMDGPUKubernetesResource]
	if !hasGPU {
		return nil, nil
	}

	totalGPUs := float64(gpuQuantity.Value())
	resourceInfo := &v1.ResourceInfo{
		AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
			v1.AcceleratorTypeAMDGPU: {
				Quantity:      totalGPUs,
				ProductGroups: map[v1.AcceleratorProduct]float64{},
			},
		},
	}

	if product, ok := labels[AMDGPUKubernetesNodeSelectorKey]; ok {
		resourceInfo.AcceleratorGroups[v1.AcceleratorTypeAMDGPU].
			ProductGroups[v1.AcceleratorProduct(product)] = totalGPUs
	}

	return resourceInfo, nil
}
