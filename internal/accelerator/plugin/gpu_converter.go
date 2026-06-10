package plugin

import (
	"fmt"
	"strconv"

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

	if spec.GetGPUCount() == 0 {
		return nil, nil
	}

	if spec.Accelerator == nil || spec.GetAcceleratorType() != string(v1.AcceleratorTypeNVIDIAGPU) {
		return nil, nil
	}

	res := &v1.RayResourceSpec{
		Resources: make(map[string]float64),
	}

	res.NumGPUs = spec.GetGPUCount()

	// Set accelerator product model as custom resource
	if product := spec.GetAcceleratorProduct(); product != "" {
		if spec.GPU != nil {
			res.Resources[product] = spec.GetGPUCount()
		}
	}

	return res, nil
}

// ConvertToKubernetes converts to Kubernetes resource configuration
func (c *GPUConverter) ConvertToKubernetes(spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("resource spec is nil")
	}

	if spec.Accelerator == nil || spec.GetAcceleratorType() != string(v1.AcceleratorTypeNVIDIAGPU) {
		return nil, nil
	}

	k8s := &v1.KubernetesResourceSpec{
		Requests:     make(map[string]string),
		Limits:       make(map[string]string),
		NodeSelector: make(map[string]string),
		Annotations:  make(map[string]string),
		Env:          make(map[string]string),
	}

	if spec.GetGPUCount() == 0 {
		k8s.Env["NVIDIA_VISIBLE_DEVICES"] = "none"
		return k8s, nil
	}

	// Set NVIDIA GPU
	gpuCount := fmt.Sprintf("%.0f", spec.GetGPUCount())
	k8s.Requests[c.kubernetesResourceName.String()] = gpuCount
	k8s.Limits[c.kubernetesResourceName.String()] = gpuCount

	// Set GPU product model as nodeSelector
	if product := spec.GetAcceleratorProduct(); product != "" {
		k8s.NodeSelector[c.nodeSelectorKey] = product
	}

	if spec.HasAcceleratorVirtualization() {
		// Keep the public API in Neutree resource terms. The HAMi raw resource
		// names are introduced only at the Kubernetes manifest boundary.
		if err := c.setHAMiVirtualizationResources(k8s, spec); err != nil {
			return nil, err
		}

		return k8s, nil
	}

	return k8s, nil
}

func (c *GPUConverter) setHAMiVirtualizationResources(k8s *v1.KubernetesResourceSpec, spec *v1.ResourceSpec) error {
	memoryMiB := spec.GetAcceleratorVirtualizationMemoryMiB()
	memoryPercent := spec.GetAcceleratorVirtualizationMemoryPercent()
	corePercent := spec.GetAcceleratorVirtualizationCorePercent()

	if memoryMiB != "" && memoryPercent != "" {
		return fmt.Errorf("%s and %s are mutually exclusive",
			v1.AcceleratorVirtualizationMemoryMiBKey,
			v1.AcceleratorVirtualizationMemoryPercentKey)
	}

	if memoryMiB != "" {
		if err := validatePositiveInteger(memoryMiB, v1.AcceleratorVirtualizationMemoryMiBKey); err != nil {
			return err
		}

		setKubernetesResource(k8s, NvidiaGPUMemoryResource.String(), memoryMiB)
	}

	if memoryPercent != "" {
		if err := validatePercent(memoryPercent, v1.AcceleratorVirtualizationMemoryPercentKey); err != nil {
			return err
		}
		// HAMi accepts percentage memory as a Pod resource, while Neutree
		// converts it back to MiB in resource views using product metadata.
		setKubernetesResource(k8s, NvidiaGPUMemoryPercentageResource.String(), memoryPercent)
	}

	if corePercent != "" {
		if err := validatePercent(corePercent, v1.AcceleratorVirtualizationCorePercentKey); err != nil {
			return err
		}

		setKubernetesResource(k8s, NvidiaGPUCoreResource.String(), corePercent)
	}

	return nil
}

func setKubernetesResource(k8s *v1.KubernetesResourceSpec, key, value string) {
	k8s.Requests[key] = value
	k8s.Limits[key] = value
}

func validatePositiveInteger(value, field string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("%s must be a positive integer", field)
	}

	return nil
}

func validatePercent(value, field string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > 100 {
		return fmt.Errorf("%s must be an integer from 1 to 100", field)
	}

	return nil
}
