package v1

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
}

// Accelerator map reserved key constants
const (
	AcceleratorTypeKey    = "type"    // Accelerator type
	AcceleratorProductKey = "product" // Accelerator product model
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

// GetCustomResources returns custom resources (excluding type and product)
func (r *ResourceSpec) GetCustomResources() map[string]string {
	if r.Accelerator == nil {
		return nil
	}

	customResources := make(map[string]string)

	for k, v := range r.Accelerator {
		if k != AcceleratorTypeKey && k != AcceleratorProductKey {
			customResources[k] = v
		}
	}

	return customResources
}

// HasAccelerator checks whether an accelerator is configured
func (r *ResourceSpec) HasAccelerator() bool {
	var gpu float64
	if r.GPU != nil {
		gpu = *r.GPU
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
