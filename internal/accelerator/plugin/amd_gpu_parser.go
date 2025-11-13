package plugin

import (
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type AMDGPUResourceParser struct {
}

// ParseFromKubernetes parses AMD GPU resources from Kubernetes node
func (p *AMDGPUResourceParser) ParseFromKubernetes(resource map[corev1.ResourceName]resource.Quantity, labels map[string]string) (*v1.ResourceInfo, error) {
	if resource == nil || labels == nil {
		return nil, errors.New("resource or label is nil")
	}

	// Check if this node has AMD GPU resources
	gpuQuantity, hasGPU := resource[AMDGPUKubernetesResource]
	if !hasGPU || gpuQuantity.IsZero() {
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

	// set product model if label exists
	if product, ok := labels[AMDGPUKubernetesNodeSelectorKey]; ok {
		resourceInfo.AcceleratorGroups[v1.AcceleratorTypeAMDGPU].ProductGroups[v1.AcceleratorProduct(product)] = totalGPUs
	}

	return resourceInfo, nil
}

// ParseFromRay parses AMD GPU resources from Ray resources
func (p *AMDGPUResourceParser) ParseFromRay(resource map[string]float64) (*v1.ResourceInfo, error) {
	if resource == nil {
		return nil, errors.New("resource is nil")
	}

	// For AMD GPUs, Ray use "GPU" and Neutree Use Real Product Names
	totalGPUs, hasGPU := resource["GPU"]
	if !hasGPU || totalGPUs <= 0 {
		return nil, nil
	}

	productName := ""
	productQuantity := 0.0
	hasAMDGPU := false

	for resourceName, quantity := range resource {
		// Try to detect AMD GPU product models from resources
		// Neutree will report AMD-specific resources like "AMD* or amd*"
		if strings.HasPrefix(resourceName, "AMD") || strings.HasPrefix(resourceName, "amd") {
			productName = resourceName
			productQuantity = quantity
			hasAMDGPU = true

			break
		}
	}

	if !hasAMDGPU {
		return nil, nil
	}

	resourceInfo := &v1.ResourceInfo{
		AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
			v1.AcceleratorTypeAMDGPU: {
				Quantity: totalGPUs,
				ProductGroups: map[v1.AcceleratorProduct]float64{
					v1.AcceleratorProduct(productName): productQuantity,
				},
			},
		},
	}

	return resourceInfo, nil
}
