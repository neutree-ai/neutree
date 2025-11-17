package plugin

import (
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type GPUResourceParser struct {
}

// ParseFromKubernetes parses NVIDIA GPU resources from Kubernetes node
func (p *GPUResourceParser) ParseFromKubernetes(resource map[corev1.ResourceName]resource.Quantity, labels map[string]string) (*v1.ResourceInfo, error) {
	if resource == nil || labels == nil {
		return nil, errors.New("resource or label is nil")
	}

	// Check if this node has NVIDIA GPU resources
	gpuQuantity, hasGPU := resource[NvidiaGPUKubernetesResource]
	if !hasGPU {
		return nil, nil
	}

	totalGPUs := float64(gpuQuantity.Value())

	resourceInfo := &v1.ResourceInfo{
		AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
			v1.AcceleratorTypeNVIDIAGPU: {
				Quantity:      totalGPUs,
				ProductGroups: map[v1.AcceleratorProduct]float64{},
			},
		},
	}

	// set GPU product model from node labels (if available)
	if product, ok := labels[NvidiaGPUKubernetesNodeSelectorKey]; ok {
		resourceInfo.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].ProductGroups[v1.AcceleratorProduct(product)] = totalGPUs
	}

	return resourceInfo, nil
}

// ParseFromRay parses NVIDIA GPU resources from Ray cluster resources
func (p *GPUResourceParser) ParseFromRay(resource map[string]float64) (*v1.ResourceInfo, error) {
	if resource == nil {
		return nil, errors.New("resource is nil")
	}

	// For NVIDIA GPUs, Ray use "GPU" and Neutree Use Real Product Names
	totalGPUs, hasGPU := resource["GPU"]
	if !hasGPU {
		return nil, nil
	}

	productName := ""
	productQuantity := 0.0
	hasNVIDIAGPU := false

	for resourceName, quantity := range resource {
		// Try to detect NVIDIA GPU product models from resources
		// Neutree will report NVIDIA-specific resources like "NVIDIA* or Tesla* or Quadro* or GeForce*"
		if strings.HasPrefix(resourceName, "NVIDIA") || strings.HasPrefix(resourceName, "Tesla") ||
			strings.HasPrefix(resourceName, "Quadro") || strings.HasPrefix(resourceName, "GeForce") {
			productName = resourceName
			productQuantity = quantity
			hasNVIDIAGPU = true

			break
		}
	}

	if !hasNVIDIAGPU {
		return nil, nil
	}

	resourceInfo := &v1.ResourceInfo{
		AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
			v1.AcceleratorTypeNVIDIAGPU: {
				Quantity: totalGPUs,
				ProductGroups: map[v1.AcceleratorProduct]float64{
					v1.AcceleratorProduct(productName): productQuantity,
				},
			},
		},
	}

	return resourceInfo, nil
}
