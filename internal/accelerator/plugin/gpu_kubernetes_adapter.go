package plugin

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
)

type GPUStandardResourceAdapter struct{}

func (a *GPUStandardResourceAdapter) MatchKubernetesNode(input resourceview.KubernetesNodeResourceContext) bool {
	if input.Labels[NvidiaGPUVirtualizationLabelKey] == "true" {
		return false
	}

	_, ok := input.AllocatableResources[NvidiaGPUKubernetesResource]
	return ok
}

func (a *GPUStandardResourceAdapter) ParseKubernetesNode(
	input resourceview.KubernetesNodeResourceContext,
) (*resourceview.KubernetesResourceAdapterResult, error) {
	allocatable, err := parseStandardNvidiaKubernetesResources(input.AllocatableResources, input.Labels)
	if err != nil {
		return nil, fmt.Errorf("failed to parse allocatable NVIDIA Kubernetes resources: %w", err)
	}

	available, err := parseStandardNvidiaKubernetesResources(input.AvailableResources, input.Labels)
	if err != nil {
		return nil, fmt.Errorf("failed to parse available NVIDIA Kubernetes resources: %w", err)
	}

	return &resourceview.KubernetesResourceAdapterResult{
		Allocatable: allocatable,
		Available:   available,
	}, nil
}

func parseStandardNvidiaKubernetesResources(
	resources map[corev1.ResourceName]resource.Quantity,
	labels map[string]string,
) (*v1.ResourceInfo, error) {
	if resources == nil || labels == nil {
		return nil, fmt.Errorf("resource or label is nil")
	}

	gpuQuantity, hasGPU := resources[NvidiaGPUKubernetesResource]
	if !hasGPU {
		return nil, nil
	}

	totalGPUs := float64(gpuQuantity.Value())
	resourceInfo := &v1.ResourceInfo{
		AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
			v1.AcceleratorTypeNVIDIAGPU: {
				Quantity: totalGPUs,
			},
		},
	}

	product, ok := labels[NvidiaGPUKubernetesNodeSelectorKey]
	if !ok {
		return resourceInfo, nil
	}

	productKey := v1.AcceleratorProduct(product)
	group := resourceInfo.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	group.ProductGroups = map[v1.AcceleratorProduct]float64{
		productKey: totalGPUs,
	}

	memoryTotalMiB := parseNodeMemoryMiB(labels[NvidiaGPUMemoryNodeLabelKey])
	if memoryTotalMiB <= 0 {
		return resourceInfo, nil
	}

	group.Products = map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
		productKey: {
			Quantity: totalGPUs,
		},
	}
	resourceInfo.AcceleratorMetadata = map[v1.AcceleratorType]*v1.AcceleratorMetadata{
		v1.AcceleratorTypeNVIDIAGPU: {
			Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata{
				productKey: {
					MemoryTotalMiB: memoryTotalMiB,
				},
			},
		},
	}

	return resourceInfo, nil
}
