package v1

import "strconv"

// RayResourceSpec represents Ray resource specification
type RayResourceSpec struct {
	NumGPUs   float64            `json:"num_gpus,omitempty" yaml:"num_gpus,omitempty"`
	NumCPUs   float64            `json:"num_cpus,omitempty" yaml:"num_cpus,omitempty"`
	Memory    float64            `json:"memory,omitempty" yaml:"memory,omitempty"`
	Resources map[string]float64 `json:"resources,omitempty" yaml:"resources,omitempty"`
}

// KubernetesResourceSpec represents Kubernetes resource specification
type KubernetesResourceSpec struct {
	Requests     map[string]string `json:"requests,omitempty" yaml:"requests,omitempty"`
	Limits       map[string]string `json:"limits,omitempty" yaml:"limits,omitempty"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty" yaml:"nodeSelector,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
	Env          map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
}

// Accelerator map reserved key constants
const (
	AcceleratorTypeKey    = "type"    // Accelerator type
	AcceleratorProductKey = "product" // Accelerator product model

	AcceleratorVirtualizationMemoryMiBKey     = "virtualization.memory_mib"
	AcceleratorVirtualizationMemoryPercentKey = "virtualization.memory_percent"
	AcceleratorVirtualizationCorePercentKey   = "virtualization.core_percent"
)

// GetAcceleratorType returns the accelerator type
func (r *ResourceSpec) GetAcceleratorType() string {
	if r.Accelerator == nil {
		return ""
	}

	return r.Accelerator[AcceleratorTypeKey]
}

// GetAcceleratorProduct returns the accelerator product model
func (r *ResourceSpec) GetAcceleratorProduct() string {
	if r.Accelerator == nil {
		return ""
	}

	return r.Accelerator[AcceleratorProductKey]
}

func (r *ResourceSpec) GetAcceleratorVirtualizationMemoryMiB() string {
	if r.Accelerator == nil {
		return ""
	}

	return r.Accelerator[AcceleratorVirtualizationMemoryMiBKey]
}

func (r *ResourceSpec) GetAcceleratorVirtualizationMemoryPercent() string {
	if r.Accelerator == nil {
		return ""
	}

	return r.Accelerator[AcceleratorVirtualizationMemoryPercentKey]
}

func (r *ResourceSpec) GetAcceleratorVirtualizationCorePercent() string {
	if r.Accelerator == nil {
		return ""
	}

	return r.Accelerator[AcceleratorVirtualizationCorePercentKey]
}

func (r *ResourceSpec) HasAcceleratorVirtualization() bool {
	return r.GetAcceleratorVirtualizationMemoryMiB() != "" ||
		r.GetAcceleratorVirtualizationMemoryPercent() != "" ||
		r.GetAcceleratorVirtualizationCorePercent() != ""
}

// GetCustomResources returns custom resources (excluding type and product)
func (r *ResourceSpec) GetCustomResources() map[string]string {
	if r.Accelerator == nil {
		return nil
	}

	customResources := make(map[string]string)

	for k, v := range r.Accelerator {
		if IsReservedKey(k) || IsAcceleratorVirtualizationKey(k) {
			continue
		}

		customResources[k] = v
	}

	return customResources
}

// HasAccelerator checks whether an accelerator is configured
func (r *ResourceSpec) HasAccelerator() bool {
	var gpu float64
	if r.GPU != nil {
		gpu, _ = strconv.ParseFloat(*r.GPU, 64)
	}

	return gpu > 0 && r.GetAcceleratorType() != ""
}

// SetAcceleratorType sets the accelerator type
func (r *ResourceSpec) SetAcceleratorType(acceleratorType string) {
	if r.Accelerator == nil {
		r.Accelerator = make(map[string]string)
	}

	r.Accelerator[AcceleratorTypeKey] = acceleratorType
}

// SetAcceleratorProduct sets the accelerator product model
func (r *ResourceSpec) SetAcceleratorProduct(product string) {
	if r.Accelerator == nil {
		r.Accelerator = make(map[string]string)
	}

	r.Accelerator[AcceleratorProductKey] = product
}

// AddCustomResource adds a custom resource
func (r *ResourceSpec) AddCustomResource(key, value string) {
	if r.Accelerator == nil {
		r.Accelerator = make(map[string]string)
	}

	r.Accelerator[key] = value
}

// IsReservedKey checks whether the key is reserved
func IsReservedKey(key string) bool {
	return key == AcceleratorTypeKey || key == AcceleratorProductKey
}

func IsAcceleratorVirtualizationKey(key string) bool {
	return key == AcceleratorVirtualizationMemoryMiBKey ||
		key == AcceleratorVirtualizationMemoryPercentKey ||
		key == AcceleratorVirtualizationCorePercentKey
}

func IsHAMiRawNvidiaResourceKey(key string) bool {
	switch key {
	case "nvidia.com/gpumem", "nvidia.com/gpumem-percentage", "nvidia.com/gpucores":
		return true
	default:
		return false
	}
}

// GetGPUCount returns the GPU count
// If GPU is nil or cannot be parsed, it returns 0.
func (r *ResourceSpec) GetGPUCount() float64 {
	if r.GPU == nil {
		return 0
	}

	gpuCount, err := strconv.ParseFloat(*r.GPU, 64)
	if err != nil {
		return 0
	}

	return gpuCount
}

// GetCPUCount returns the CPU count
// If CPU is nil or cannot be parsed, it returns 0.
func (r *ResourceSpec) GetCPUCount() float64 {
	if r.CPU == nil {
		return 0
	}

	cpuCount, err := strconv.ParseFloat(*r.CPU, 64)
	if err != nil {
		return 0
	}

	return cpuCount
}

// GetMemoryInGB returns the memory in GB
// If Memory is nil or cannot be parsed, it returns 0.
func (r *ResourceSpec) GetMemoryInGB() float64 {
	if r.Memory == nil {
		return 0
	}

	memoryInGB, err := strconv.ParseFloat(*r.Memory, 64)
	if err != nil {
		return 0
	}

	return memoryInGB
}
