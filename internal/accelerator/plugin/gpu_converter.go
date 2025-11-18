package plugin

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// GPUConverter is the NVIDIA GPU resource converter
type GPUConverter struct {
	kubernetesResourceName corev1.ResourceName
	nodeSelectorKey        string
}

// NewGPUConverter creates a new NVIDIA GPU converter
func NewGPUConverter() *GPUConverter {
	return &GPUConverter{
		kubernetesResourceName: NvidiaGPUKubernetesResource,
		nodeSelectorKey:        NvidiaGPUKubernetesNodeSelectorKey,
	}
}

// ConvertToRay converts to Ray resource configuration
func (c *GPUConverter) ConvertToRay(spec *v1.ResourceSpec) (*v1.RayResourceSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("resource spec is nil")
	}

	if spec.GPU == nil || *spec.GPU <= 0 {
		return nil, nil
	}

	if spec.Accelerator == nil || spec.GetAcceleratorType() != string(v1.AcceleratorTypeNVIDIAGPU) {
		return nil, nil
	}

	res := &v1.RayResourceSpec{
		Resources: make(map[string]float64),
	}

	res.NumGPUs = *spec.GPU

	// Set accelerator product model as custom resource
	if product := spec.GetAcceleratorProduct(); product != "" {
		if spec.GPU != nil {
			res.Resources[product] = float64(*spec.GPU)
		}
	}

	return res, nil
}

// ConvertToKubernetes converts to Kubernetes resource configuration
func (c *GPUConverter) ConvertToKubernetes(spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("resource spec is nil")
	}

	if spec.GPU == nil || *spec.GPU <= 0 {
		return nil, nil
	}

	if spec.Accelerator == nil || spec.GetAcceleratorType() != string(v1.AcceleratorTypeNVIDIAGPU) {
		return nil, nil
	}

	k8s := &v1.KubernetesResourceSpec{
		Requests:     make(map[string]string),
		Limits:       make(map[string]string),
		NodeSelector: make(map[string]string),
	}

	// Set NVIDIA GPU
	gpuCount := fmt.Sprintf("%.0f", *spec.GPU)
	k8s.Requests[c.kubernetesResourceName.String()] = gpuCount
	k8s.Limits[c.kubernetesResourceName.String()] = gpuCount

	// Set GPU product model as nodeSelector
	if product := spec.GetAcceleratorProduct(); product != "" {
		k8s.NodeSelector[c.nodeSelectorKey] = product
	}

	return k8s, nil
}
