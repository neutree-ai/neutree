package plugin

import (
	"fmt"
	"strconv"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	amdGPUKubernetesResource = "amd.com/gpu"
	amdGPUNodeSelectorKey    = "amd.com/gpu.product-name"
)

// AMDGPUConverter is the AMD GPU resource converter
type AMDGPUConverter struct {
	kubernetesResourceName string
	nodeSelectorKey        string
}

// NewAMDGPUConverter creates a new AMD GPU converter
func NewAMDGPUConverter() *AMDGPUConverter {
	return &AMDGPUConverter{
		kubernetesResourceName: amdGPUKubernetesResource,
		nodeSelectorKey:        amdGPUNodeSelectorKey,
	}
}

// ConvertToRay converts to Ray resource configuration
func (c *AMDGPUConverter) ConvertToRay(spec *v1.ResourceSpec) (*v1.RayResourceSpec, error) {
	ray := &v1.RayResourceSpec{
		Resources: make(map[string]float64),
	}

	// Set CPU
	if spec.CPU != nil {
		ray.NumCPUs = *spec.CPU
	}

	// Set memory
	if spec.Memory != nil {
		ray.Memory = float64(*spec.Memory) * BytesPerGiB
	}

	// Set GPU count
	if spec.GPU != nil && *spec.GPU > 0 {
		ray.NumGPUs = *spec.GPU
	}

	// Set accelerator product model as custom resource
	if product := spec.GetAcceleratorProduct(); product != "" {
		ray.Resources[product] = *spec.GPU
	}

	// Add all custom resources (excluding type and product)
	for k, v := range spec.GetCustomResources() {
		// Try to convert to number
		if floatVal, err := strconv.ParseFloat(v, 64); err == nil {
			ray.Resources[k] = floatVal
		} else {
			klog.Warningf("Failed to parse custom resource %s value %s to float: %v", k, v, err)
		}
	}

	return ray, nil
}

// ConvertToKubernetes converts to Kubernetes resource configuration
func (c *AMDGPUConverter) ConvertToKubernetes(spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error) {
	k8s := &v1.KubernetesResourceSpec{
		Requests:     make(map[string]string),
		Limits:       make(map[string]string),
		NodeSelector: make(map[string]string),
	}

	// Set CPU
	if spec.CPU != nil && *spec.CPU > 0 {
		cpuStr := fmt.Sprintf("%.0f", *spec.CPU)
		k8s.Requests["cpu"] = cpuStr
		k8s.Limits["cpu"] = cpuStr
	}

	// Set memory
	if spec.Memory != nil && *spec.Memory > 0 {
		memoryStr := fmt.Sprintf("%.0fGi", *spec.Memory)
		k8s.Requests["memory"] = memoryStr
		k8s.Limits["memory"] = memoryStr
	}

	// Set AMD GPU
	if spec.GPU != nil && *spec.GPU > 0 {
		gpuCount := fmt.Sprintf("%.0f", *spec.GPU)
		k8s.Requests[c.kubernetesResourceName] = gpuCount
		k8s.Limits[c.kubernetesResourceName] = gpuCount
	}

	// Set GPU product model as nodeSelector
	if product := spec.GetAcceleratorProduct(); product != "" {
		k8s.NodeSelector[c.nodeSelectorKey] = product
	}

	// Add all custom resources
	for k, v := range spec.GetCustomResources() {
		k8s.Requests[k] = v
		k8s.Limits[k] = v
	}

	return k8s, nil
}
