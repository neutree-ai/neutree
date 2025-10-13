package resource

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

// ConverterManager is the interface for resource conversion manager
type ConverterManager interface {
	// ConvertToRay converts to Ray resource configuration
	ConvertToRay(ctx context.Context, spec *v1.ResourceSpec) (*v1.RayResourceSpec, error)

	// ConvertToKubernetes converts to Kubernetes resource configuration
	ConvertToKubernetes(ctx context.Context, spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error)

	// RegisterConverter registers a converter
	RegisterConverter(acceleratorType string, converter v1.ResourceConverter) error

	// ListConverterTypes lists all registered converter types
	ListConverterTypes() []string
}

// converterManager implements resource conversion manager
type converterManager struct {
	mu         sync.RWMutex
	converters map[string]v1.ResourceConverter // key: accelerator_type
}

// NewConverterManager creates a new converter manager
func NewConverterManager() ConverterManager {
	return &converterManager{
		converters: make(map[string]v1.ResourceConverter),
	}
}

// RegisterConverter registers a converter
func (cm *converterManager) RegisterConverter(acceleratorType string, converter v1.ResourceConverter) error {
	if acceleratorType == "" {
		return fmt.Errorf("accelerator type cannot be empty")
	}

	if converter == nil {
		return fmt.Errorf("converter cannot be nil")
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, exists := cm.converters[acceleratorType]; exists {
		klog.Warningf("Converter for accelerator type %s already exists, will be overwritten", acceleratorType)
	}

	cm.converters[acceleratorType] = converter

	klog.V(4).InfoS("Registered resource converter", "acceleratorType", acceleratorType)

	return nil
}

// ListConverterTypes lists all registered converter types
func (cm *converterManager) ListConverterTypes() []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	types := make([]string, 0, len(cm.converters))
	for t := range cm.converters {
		types = append(types, t)
	}

	return types
}

// ConvertToRay converts to Ray resource configuration
func (cm *converterManager) ConvertToRay(ctx context.Context, spec *v1.ResourceSpec) (*v1.RayResourceSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("resource spec cannot be nil")
	}

	// Get accelerator type
	acceleratorType := spec.GetAcceleratorType()

	// If no accelerator type is configured, return CPU-only configuration
	if acceleratorType == "" {
		klog.V(4).InfoS("No accelerator type specified, using CPU-only configuration")
		return cm.convertCPUOnlyToRay(spec), nil
	}

	klog.V(4).InfoS("Converting resource spec to Ray",
		"acceleratorType", acceleratorType,
		"gpu", spec.GPU,
		"cpu", spec.CPU,
	)

	// Find converter
	cm.mu.RLock()
	converter, ok := cm.converters[acceleratorType]
	cm.mu.RUnlock()

	if !ok {
		availableTypes := cm.ListConverterTypes()
		err := fmt.Errorf("no converter found for accelerator type: %s", acceleratorType)
		klog.ErrorS(err, "Conversion failed",
			"acceleratorType", acceleratorType,
			"availableConverters", availableTypes,
		)

		return nil, err
	}

	// Execute conversion
	result, err := converter.ConvertToRay(spec)
	if err != nil {
		klog.ErrorS(err, "Converter execution failed",
			"acceleratorType", acceleratorType,
			"spec", spec,
		)

		return nil, fmt.Errorf("conversion failed for %s: %w", acceleratorType, err)
	}

	klog.V(4).InfoS("Conversion successful",
		"acceleratorType", acceleratorType,
		"numGPUs", result.NumGPUs,
		"numCPUs", result.NumCPUs,
	)

	return result, nil
}

// ConvertToKubernetes converts to Kubernetes resource configuration
func (cm *converterManager) ConvertToKubernetes(ctx context.Context, spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("resource spec cannot be nil")
	}

	// Get accelerator type
	acceleratorType := spec.GetAcceleratorType()

	// If no accelerator type is configured, return CPU-only configuration
	if acceleratorType == "" {
		klog.V(4).InfoS("No accelerator type specified, using CPU-only configuration")
		return cm.convertCPUOnlyToKubernetes(spec), nil
	}

	klog.V(4).InfoS("Converting resource spec to Kubernetes",
		"acceleratorType", acceleratorType,
		"gpu", spec.GPU,
		"cpu", spec.CPU,
	)

	// Find converter
	cm.mu.RLock()
	converter, ok := cm.converters[acceleratorType]
	cm.mu.RUnlock()

	if !ok {
		availableTypes := cm.ListConverterTypes()
		err := fmt.Errorf("no converter found for accelerator type: %s", acceleratorType)
		klog.ErrorS(err, "Conversion failed",
			"acceleratorType", acceleratorType,
			"availableConverters", availableTypes,
		)

		return nil, err
	}

	// Execute conversion
	result, err := converter.ConvertToKubernetes(spec)
	if err != nil {
		klog.ErrorS(err, "Converter execution failed",
			"acceleratorType", acceleratorType,
			"spec", spec,
		)

		return nil, fmt.Errorf("conversion failed for %s: %w", acceleratorType, err)
	}

	klog.V(4).InfoS("Conversion successful",
		"acceleratorType", acceleratorType,
		"requests", result.Requests,
	)

	return result, nil
}

// convertCPUOnlyToRay converts CPU-only scenario to Ray configuration
func (cm *converterManager) convertCPUOnlyToRay(spec *v1.ResourceSpec) *v1.RayResourceSpec {
	ray := &v1.RayResourceSpec{
		Resources: make(map[string]float64),
	}

	if spec.CPU != nil {
		ray.NumCPUs = *spec.CPU
	}

	if spec.Memory != nil {
		ray.Memory = *spec.Memory * plugin.BytesPerGiB
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

	return ray
}

// convertCPUOnlyToKubernetes converts CPU-only scenario to Kubernetes configuration
func (cm *converterManager) convertCPUOnlyToKubernetes(spec *v1.ResourceSpec) *v1.KubernetesResourceSpec {
	k8s := &v1.KubernetesResourceSpec{
		Requests:     make(map[string]string),
		Limits:       make(map[string]string),
		NodeSelector: make(map[string]string),
	}

	if spec.CPU != nil && *spec.CPU > 0 {
		cpuStr := fmt.Sprintf("%.0f", *spec.CPU)
		k8s.Requests["cpu"] = cpuStr
		k8s.Limits["cpu"] = cpuStr
	}

	if spec.Memory != nil && *spec.Memory > 0 {
		memoryStr := fmt.Sprintf("%.0fGi", *spec.Memory)
		k8s.Requests["memory"] = memoryStr
		k8s.Limits["memory"] = memoryStr
	}

	// Add custom resources
	for k, v := range spec.GetCustomResources() {
		k8s.Requests[k] = v
		k8s.Limits[k] = v
	}

	return k8s
}
