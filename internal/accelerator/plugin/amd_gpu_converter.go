package plugin

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// AMDGPUConverter is the AMD GPU resource converter
type AMDGPUConverter struct {
	kubernetesResourceName corev1.ResourceName
	nodeSelectorKey        string
}

// NewAMDGPUConverter creates a new AMD GPU converter
func NewAMDGPUConverter() *AMDGPUConverter {
	return &AMDGPUConverter{
		kubernetesResourceName: AMDGPUKubernetesResource,
		nodeSelectorKey:        AMDGPUKubernetesNodeSelectorKey,
	}
}

// ConvertToRay converts to Ray resource configuration
func (c *AMDGPUConverter) ConvertToRay(spec *v1.ResourceSpec) (*v1.RayResourceSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("resource spec is nil")
	}

	if spec.GPU == nil || *spec.GPU <= 0 {
		return nil, nil
	}

	if spec.Accelerator == nil || spec.GetAcceleratorType() != string(v1.AcceleratorTypeAMDGPU) {
		return nil, nil
	}

	ray := &v1.RayResourceSpec{
		Resources: make(map[string]float64),
	}

	// Set GPU count
	if spec.GPU != nil && *spec.GPU > 0 {
		ray.NumGPUs = *spec.GPU
	}

	// Set accelerator product model as custom resource
	if product := spec.GetAcceleratorProduct(); product != "" {
		ray.Resources[product] = *spec.GPU
	}

	return ray, nil
}

// ConvertToKubernetes converts to Kubernetes resource configuration
func (c *AMDGPUConverter) ConvertToKubernetes(spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("resource spec is nil")
	}

	if spec.GPU == nil || *spec.GPU <= 0 {
		return nil, nil
	}

	if spec.Accelerator == nil || spec.GetAcceleratorType() != string(v1.AcceleratorTypeAMDGPU) {
		return nil, nil
	}

	res := &v1.KubernetesResourceSpec{
		Requests:     make(map[string]string),
		Limits:       make(map[string]string),
		NodeSelector: make(map[string]string),
	}

	// Set AMD GPU
	gpuCount := fmt.Sprintf("%.0f", *spec.GPU)
	res.Requests[c.kubernetesResourceName.String()] = gpuCount
	res.Limits[c.kubernetesResourceName.String()] = gpuCount

	// Set GPU product model as nodeSelector
	if product := spec.GetAcceleratorProduct(); product != "" {
		res.NodeSelector[c.nodeSelectorKey] = product
	}

	return res, nil
}
