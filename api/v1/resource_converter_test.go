package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/pointer"
)

// TestGetAcceleratorType tests the GetAcceleratorType method
func TestGetAcceleratorType(t *testing.T) {
	tests := []struct {
		name     string
		resource *ResourceSpec
		expected string
	}{
		{
			name: "accelerator with type",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorTypeKey: "nvidia",
				},
			},
			expected: "nvidia",
		},
		{
			name:     "nil accelerator",
			resource: &ResourceSpec{},
			expected: "",
		},
		{
			name: "empty accelerator map",
			resource: &ResourceSpec{
				Accelerator: map[string]string{},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.resource.GetAcceleratorType()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetAcceleratorProduct tests the GetAcceleratorProduct method
func TestGetAcceleratorProduct(t *testing.T) {
	tests := []struct {
		name     string
		resource *ResourceSpec
		expected string
	}{
		{
			name: "accelerator with product",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorProductKey: "a100",
				},
			},
			expected: "a100",
		},
		{
			name:     "nil accelerator",
			resource: &ResourceSpec{},
			expected: "",
		},
		{
			name: "empty accelerator map",
			resource: &ResourceSpec{
				Accelerator: map[string]string{},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.resource.GetAcceleratorProduct()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetCustomResources tests the GetCustomResources method
func TestGetCustomResources(t *testing.T) {
	tests := []struct {
		name     string
		resource *ResourceSpec
		expected map[string]string
	}{
		{
			name: "accelerator with custom resources",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorTypeKey:    "nvidia",
					AcceleratorProductKey: "a100",
					"memory":              "40GB",
					"compute_capability":  "8.0",
				},
			},
			expected: map[string]string{
				"memory":             "40GB",
				"compute_capability": "8.0",
			},
		},
		{
			name: "accelerator with only reserved keys",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorTypeKey:    "nvidia",
					AcceleratorProductKey: "a100",
				},
			},
			expected: map[string]string{},
		},
		{
			name: "accelerator with only custom resources",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					"memory":    "80GB",
					"bandwidth": "1.5TB/s",
				},
			},
			expected: map[string]string{
				"memory":    "80GB",
				"bandwidth": "1.5TB/s",
			},
		},
		{
			name:     "nil accelerator",
			resource: &ResourceSpec{},
			expected: nil,
		},
		{
			name: "empty accelerator map",
			resource: &ResourceSpec{
				Accelerator: map[string]string{},
			},
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.resource.GetCustomResources()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestHasAccelerator tests the HasAccelerator method
func TestHasAccelerator(t *testing.T) {
	tests := []struct {
		name     string
		resource *ResourceSpec
		expected bool
	}{
		{
			name: "has GPU and accelerator type",
			resource: &ResourceSpec{
				GPU: pointer.String("2"),
				Accelerator: map[string]string{
					AcceleratorTypeKey: "nvidia",
				},
			},
			expected: true,
		},
		{
			name: "has GPU but no accelerator type",
			resource: &ResourceSpec{
				GPU: pointer.String("2"),
				Accelerator: map[string]string{
					AcceleratorProductKey: "a100",
				},
			},
			expected: false,
		},
		{
			name: "has accelerator type but no GPU",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorTypeKey: "nvidia",
				},
			},
			expected: false,
		},
		{
			name: "has GPU=0 and accelerator type",
			resource: &ResourceSpec{
				GPU: pointer.String("0"),
				Accelerator: map[string]string{
					AcceleratorTypeKey: "nvidia",
				},
			},
			expected: false,
		},
		{
			name: "has fractional GPU and accelerator type",
			resource: &ResourceSpec{
				GPU: pointer.String("0.5"),
				Accelerator: map[string]string{
					AcceleratorTypeKey: "nvidia",
				},
			},
			expected: true,
		},
		{
			name:     "nil GPU and nil accelerator",
			resource: &ResourceSpec{},
			expected: false,
		},
		{
			name: "nil GPU with accelerator type",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorTypeKey: "nvidia",
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.resource.HasAccelerator()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestSetAcceleratorType tests the SetAcceleratorType method
func TestSetAcceleratorType(t *testing.T) {
	tests := []struct {
		name            string
		resource        *ResourceSpec
		acceleratorType string
		validate        func(t *testing.T, r *ResourceSpec)
	}{
		{
			name:            "set type on nil accelerator",
			resource:        &ResourceSpec{},
			acceleratorType: "nvidia_gpu",
			validate: func(t *testing.T, r *ResourceSpec) {
				require.NotNil(t, r.Accelerator)
				assert.Equal(t, "nvidia_gpu", r.Accelerator[AcceleratorTypeKey])
			},
		},
		{
			name: "set type on existing accelerator",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorProductKey: "a100",
				},
			},
			acceleratorType: "nvidia_gpu",
			validate: func(t *testing.T, r *ResourceSpec) {
				assert.Equal(t, "nvidia_gpu", r.Accelerator[AcceleratorTypeKey])
				assert.Equal(t, "a100", r.Accelerator[AcceleratorProductKey])
			},
		},
		{
			name: "overwrite existing type",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorTypeKey: "amd_gpu",
				},
			},
			acceleratorType: "nvidia_gpu",
			validate: func(t *testing.T, r *ResourceSpec) {
				assert.Equal(t, "nvidia_gpu", r.Accelerator[AcceleratorTypeKey])
			},
		},
		{
			name:            "set empty type",
			resource:        &ResourceSpec{},
			acceleratorType: "",
			validate: func(t *testing.T, r *ResourceSpec) {
				require.NotNil(t, r.Accelerator)
				assert.Equal(t, "", r.Accelerator[AcceleratorTypeKey])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.resource.SetAcceleratorType(tt.acceleratorType)
			tt.validate(t, tt.resource)
		})
	}
}

// TestSetAcceleratorProduct tests the SetAcceleratorProduct method
func TestSetAcceleratorProduct(t *testing.T) {
	tests := []struct {
		name     string
		resource *ResourceSpec
		product  string
		validate func(t *testing.T, r *ResourceSpec)
	}{
		{
			name:     "set product on nil accelerator",
			resource: &ResourceSpec{},
			product:  "a100",
			validate: func(t *testing.T, r *ResourceSpec) {
				require.NotNil(t, r.Accelerator)
				assert.Equal(t, "a100", r.Accelerator[AcceleratorProductKey])
			},
		},
		{
			name: "set product on existing accelerator",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorTypeKey: "nvidia_gpu",
				},
			},
			product: "v100",
			validate: func(t *testing.T, r *ResourceSpec) {
				assert.Equal(t, "v100", r.Accelerator[AcceleratorProductKey])
				assert.Equal(t, "nvidia_gpu", r.Accelerator[AcceleratorTypeKey])
			},
		},
		{
			name: "overwrite existing product",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorProductKey: "v100",
				},
			},
			product: "a100",
			validate: func(t *testing.T, r *ResourceSpec) {
				assert.Equal(t, "a100", r.Accelerator[AcceleratorProductKey])
			},
		},
		{
			name:     "set empty product",
			resource: &ResourceSpec{},
			product:  "",
			validate: func(t *testing.T, r *ResourceSpec) {
				require.NotNil(t, r.Accelerator)
				assert.Equal(t, "", r.Accelerator[AcceleratorProductKey])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.resource.SetAcceleratorProduct(tt.product)
			tt.validate(t, tt.resource)
		})
	}
}

// TestAddCustomResource tests the AddCustomResource method
func TestAddCustomResource(t *testing.T) {
	tests := []struct {
		name     string
		resource *ResourceSpec
		key      string
		value    string
		validate func(t *testing.T, r *ResourceSpec)
	}{
		{
			name:     "add custom resource to nil accelerator",
			resource: &ResourceSpec{},
			key:      "memory",
			value:    "40GB",
			validate: func(t *testing.T, r *ResourceSpec) {
				require.NotNil(t, r.Accelerator)
				assert.Equal(t, "40GB", r.Accelerator["memory"])
			},
		},
		{
			name: "add custom resource to existing accelerator",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorTypeKey: "nvidia",
				},
			},
			key:   "compute_capability",
			value: "8.0",
			validate: func(t *testing.T, r *ResourceSpec) {
				assert.Equal(t, "8.0", r.Accelerator["compute_capability"])
				assert.Equal(t, "nvidia", r.Accelerator[AcceleratorTypeKey])
			},
		},
		{
			name: "overwrite existing custom resource",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					"memory": "40GB",
				},
			},
			key:   "memory",
			value: "80GB",
			validate: func(t *testing.T, r *ResourceSpec) {
				assert.Equal(t, "80GB", r.Accelerator["memory"])
			},
		},
		{
			name:     "add reserved key as custom resource (allowed but not recommended)",
			resource: &ResourceSpec{},
			key:      AcceleratorTypeKey,
			value:    "nvidia_gpu",
			validate: func(t *testing.T, r *ResourceSpec) {
				require.NotNil(t, r.Accelerator)
				assert.Equal(t, "nvidia_gpu", r.Accelerator[AcceleratorTypeKey])
			},
		},
		{
			name: "add multiple custom resources",
			resource: &ResourceSpec{
				Accelerator: map[string]string{
					AcceleratorTypeKey: "nvidia_gpu",
				},
			},
			key:   "bandwidth",
			value: "1.5TB/s",
			validate: func(t *testing.T, r *ResourceSpec) {
				// Add another one to test multiple additions
				r.AddCustomResource("memory", "40GB")
				assert.Equal(t, "1.5TB/s", r.Accelerator["bandwidth"])
				assert.Equal(t, "40GB", r.Accelerator["memory"])
				assert.Equal(t, "nvidia_gpu", r.Accelerator[AcceleratorTypeKey])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.resource.AddCustomResource(tt.key, tt.value)
			tt.validate(t, tt.resource)
		})
	}
}

// TestIsReservedKey tests the IsReservedKey function
func TestIsReservedKey(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		expected bool
	}{
		{
			name:     "type key is reserved",
			key:      AcceleratorTypeKey,
			expected: true,
		},
		{
			name:     "product key is reserved",
			key:      AcceleratorProductKey,
			expected: true,
		},
		{
			name:     "custom key is not reserved",
			key:      "memory",
			expected: false,
		},
		{
			name:     "empty key is not reserved",
			key:      "",
			expected: false,
		},
		{
			name:     "random key is not reserved",
			key:      "compute_capability",
			expected: false,
		},
		{
			name:     "case sensitive - Type is not reserved",
			key:      "Type",
			expected: false,
		},
		{
			name:     "case sensitive - Product is not reserved",
			key:      "Product",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsReservedKey(tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestResourceSpecIntegration tests combined operations
func TestResourceSpecIntegration(t *testing.T) {
	tests := []struct {
		name     string
		setup    func() *ResourceSpec
		validate func(t *testing.T, r *ResourceSpec)
	}{
		{
			name: "build complete accelerator spec",
			setup: func() *ResourceSpec {
				r := &ResourceSpec{
					CPU:    pointer.String("4"),
					GPU:    pointer.String("2"),
					Memory: pointer.String("16"),
				}
				r.SetAcceleratorType("nvidia_gpu")
				r.SetAcceleratorProduct("a100")
				r.AddCustomResource("memory", "40GB")
				r.AddCustomResource("compute_capability", "8.0")
				return r
			},
			validate: func(t *testing.T, r *ResourceSpec) {
				assert.Equal(t, "nvidia_gpu", r.GetAcceleratorType())
				assert.Equal(t, "a100", r.GetAcceleratorProduct())
				assert.True(t, r.HasAccelerator())

				custom := r.GetCustomResources()
				assert.Equal(t, 2, len(custom))
				assert.Equal(t, "40GB", custom["memory"])
				assert.Equal(t, "8.0", custom["compute_capability"])
			},
		},
		{
			name: "modify existing accelerator",
			setup: func() *ResourceSpec {
				r := &ResourceSpec{
					GPU: pointer.String("1"),
					Accelerator: map[string]string{
						AcceleratorTypeKey:    "amd_gpu",
						AcceleratorProductKey: "mi100",
						"memory":              "32GB",
					},
				}
				// Change to nvidia
				r.SetAcceleratorType("nvidia_gpu")
				r.SetAcceleratorProduct("a100")
				r.AddCustomResource("memory", "40GB")
				return r
			},
			validate: func(t *testing.T, r *ResourceSpec) {
				assert.Equal(t, "nvidia_gpu", r.GetAcceleratorType())
				assert.Equal(t, "a100", r.GetAcceleratorProduct())

				custom := r.GetCustomResources()
				assert.Equal(t, 1, len(custom))
				assert.Equal(t, "40GB", custom["memory"])
			},
		},
		{
			name: "no accelerator configured",
			setup: func() *ResourceSpec {
				return &ResourceSpec{
					CPU:    pointer.String("8"),
					Memory: pointer.String("32"),
				}
			},
			validate: func(t *testing.T, r *ResourceSpec) {
				assert.Equal(t, "", r.GetAcceleratorType())
				assert.Equal(t, "", r.GetAcceleratorProduct())
				assert.False(t, r.HasAccelerator())
				assert.Nil(t, r.GetCustomResources())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.setup()
			tt.validate(t, r)
		})
	}
}

func Test_GetGPUCount(t *testing.T) {
	tests := []struct {
		name     string
		resource *ResourceSpec
		expected float64
	}{
		{
			name:     "nil GPU",
			resource: &ResourceSpec{},
			expected: 0,
		},
		{
			name: "zero GPU",
			resource: &ResourceSpec{
				GPU: pointer.String("0"),
			},
			expected: 0,
		},
		{
			name: "positive integer GPU",
			resource: &ResourceSpec{
				GPU: pointer.String("4"),
			},
			expected: 4,
		},
		{
			name: "positive fractional GPU",
			resource: &ResourceSpec{
				GPU: pointer.String("2.5"),
			},
			expected: 2.5,
		},
		{
			name: "invalid GPU value",
			resource: &ResourceSpec{
				GPU: pointer.String("invalid"),
			},
			expected: 0,
		},
		{
			name: "empty GPU string",
			resource: &ResourceSpec{
				GPU: pointer.String(""),
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.resource.GetGPUCount()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func Test_GetCPUCount(t *testing.T) {
	tests := []struct {
		name     string
		resource *ResourceSpec
		expected float64
	}{
		{
			name:     "nil CPU",
			resource: &ResourceSpec{},
			expected: 0,
		},
		{
			name: "zero CPU",
			resource: &ResourceSpec{
				CPU: pointer.String("0"),
			},
			expected: 0,
		},
		{
			name: "positive integer CPU",
			resource: &ResourceSpec{
				CPU: pointer.String("8"),
			},
			expected: 8,
		},
		{
			name: "positive fractional CPU",
			resource: &ResourceSpec{
				CPU: pointer.String("4.5"),
			},
			expected: 4.5,
		},
		{
			name: "invalid CPU value",
			resource: &ResourceSpec{
				CPU: pointer.String("invalid"),
			},
			expected: 0,
		},
		{
			name: "empty CPU string",
			resource: &ResourceSpec{
				CPU: pointer.String(""),
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.resource.GetCPUCount()
			assert.Equal(t, tt.expected, result)
		})
	}

}

func Test_GetMemoryInGB(t *testing.T) {
	tests := []struct {
		name     string
		resource *ResourceSpec
		expected float64
	}{
		{
			name:     "nil Memory",
			resource: &ResourceSpec{},
			expected: 0,
		},
		{
			name: "zero Memory",
			resource: &ResourceSpec{
				Memory: pointer.String("0"),
			},
			expected: 0,
		},
		{
			name: "positive integer Memory",
			resource: &ResourceSpec{
				Memory: pointer.String("16"),
			},
			expected: 16,
		},
		{
			name: "positive fractional Memory",
			resource: &ResourceSpec{
				Memory: pointer.String("12.5"),
			},
			expected: 12.5,
		},
		{
			name: "invalid Memory value",
			resource: &ResourceSpec{
				Memory: pointer.String("invalid"),
			},
			expected: 0,
		},
		{
			name: "empty Memory string",
			resource: &ResourceSpec{
				Memory: pointer.String(""),
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.resource.GetMemoryInGB()
			assert.Equal(t, tt.expected, result)
		})
	}
}
