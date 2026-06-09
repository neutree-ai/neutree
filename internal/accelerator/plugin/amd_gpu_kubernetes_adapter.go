package plugin

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
)

type AMDGPUStandardResourceAdapter struct{}

func (p *AMDGPUResourceParser) KubernetesResourceAdapters(
	_ resourceview.KubernetesResourceAdapterContext,
) []resourceview.KubernetesResourceAdapter {
	return []resourceview.KubernetesResourceAdapter{
		&AMDGPUStandardResourceAdapter{},
	}
}

func (a *AMDGPUStandardResourceAdapter) MatchKubernetesNode(input resourceview.KubernetesNodeResourceContext) bool {
	_, ok := input.AllocatableResources[AMDGPUKubernetesResource]
	return ok
}

func (a *AMDGPUStandardResourceAdapter) ParseKubernetesNode(
	input resourceview.KubernetesNodeResourceContext,
) (*resourceview.KubernetesResourceAdapterResult, error) {
	allocatable, err := parseStandardAMDKubernetesResources(input.AllocatableResources, input.Labels)
	if err != nil {
		return nil, fmt.Errorf("failed to parse allocatable AMD Kubernetes resources: %w", err)
	}

	available, err := parseStandardAMDKubernetesResources(input.AvailableResources, input.Labels)
	if err != nil {
		return nil, fmt.Errorf("failed to parse available AMD Kubernetes resources: %w", err)
	}

	return &resourceview.KubernetesResourceAdapterResult{
		Allocatable: allocatable,
		Available:   available,
	}, nil
}

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
