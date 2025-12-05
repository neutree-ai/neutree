package orchestrator

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/pkg/model_registry"
	modelregistrymocks "github.com/neutree-ai/neutree/pkg/model_registry/mocks"
)

func TestConverterManager_ConvertToRay_NVIDIA(t *testing.T) {

	mgr := &acceleratormocks.MockManager{}
	mgr.On("GetConverter", "nvidia_gpu").Return(plugin.NewGPUConverter(), true)

	gpu := float64(2)
	cpu := float64(16)
	memory := float64(64)
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(string(v1.AcceleratorTypeNVIDIAGPU))
	spec.SetAcceleratorProduct("NVIDIA-L20")
	spec.AddCustomResource("rdma/hca", "2")

	ray, err := convertToRay(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(2), ray.NumGPUs)
	assert.Equal(t, float64(16), ray.NumCPUs)
	assert.Equal(t, float64(64*plugin.BytesPerGiB), ray.Memory)
	assert.Equal(t, float64(2), ray.Resources["NVIDIA-L20"])
	assert.Equal(t, float64(2), ray.Resources["rdma/hca"])
}

func TestConverterManager_ConvertToKubernetes_NVIDIA(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	mgr.On("GetConverter", "nvidia_gpu").Return(plugin.NewGPUConverter(), true)

	gpu := float64(1)
	cpu := float64(8)
	memory := float64(32)
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(string(v1.AcceleratorTypeNVIDIAGPU))
	spec.SetAcceleratorProduct("NVIDIA-L20")

	k8s, err := convertToKubernetes(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "1", k8s.Requests["nvidia.com/gpu"])
	assert.Equal(t, "1", k8s.Limits["nvidia.com/gpu"])
	assert.Equal(t, "8", k8s.Requests["cpu"])
	assert.Equal(t, "32Gi", k8s.Requests["memory"])
	assert.Equal(t, "NVIDIA-L20", k8s.NodeSelector["nvidia.com/gpu.product"])
}

func TestConverterManager_ConvertToRay_AMD(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	mgr.On("GetConverter", "amd_gpu").Return(plugin.NewAMDGPUConverter(), true)

	gpu := float64(1)
	cpu := float64(8)
	memory := float64(32)
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(string(v1.AcceleratorTypeAMDGPU))
	spec.SetAcceleratorProduct("AMD_Instinct_MI300X_VF")

	ray, err := convertToRay(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(1), ray.NumGPUs)
	assert.Equal(t, float64(8), ray.NumCPUs)
	assert.Equal(t, float64(1), ray.Resources["AMD_Instinct_MI300X_VF"])
}

func TestConverterManager_ConvertToKubernetes_AMD(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	mgr.On("GetConverter", "amd_gpu").Return(plugin.NewAMDGPUConverter(), true)

	gpu := float64(1)
	cpu := float64(8)
	memory := float64(32)
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(string(v1.AcceleratorTypeAMDGPU))
	spec.SetAcceleratorProduct("AMD_Instinct_MI300X_VF")
	spec.AddCustomResource("hugepages-2Mi", "1024Mi")

	k8s, err := convertToKubernetes(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "1", k8s.Requests["amd.com/gpu"])
	assert.Equal(t, "1", k8s.Limits["amd.com/gpu"])
	assert.Equal(t, "AMD_Instinct_MI300X_VF", k8s.NodeSelector["amd.com/gpu.product-name"])
	assert.Equal(t, "1024Mi", k8s.Requests["hugepages-2Mi"])
}

func TestConverterManager_CPUOnly(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	cpu := float64(4)
	memory := float64(8)
	spec := &v1.ResourceSpec{
		CPU:    &cpu,
		Memory: &memory,
	}

	ray, err := convertToRay(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(4), ray.NumCPUs)
	assert.Equal(t, float64(8*plugin.BytesPerGiB), ray.Memory)

	k8s, err := convertToKubernetes(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "4", k8s.Requests["cpu"])
	assert.Equal(t, "8Gi", k8s.Requests["memory"])
}

func TestCPUOnly_MinimalConfig(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	spec := &v1.ResourceSpec{}

	ray, err := convertToRay(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(0), ray.NumCPUs)
	assert.Equal(t, float64(0), ray.Memory)

	k8s, err := convertToKubernetes(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Empty(t, k8s.Requests)
	assert.Empty(t, k8s.Limits)
}

func TestConverterManager_CPUOnly_OnlyCPU(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	cpu := float64(2)
	spec := &v1.ResourceSpec{
		CPU: &cpu,
	}

	ray, err := convertToRay(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(2), ray.NumCPUs)
	assert.Equal(t, float64(0), ray.Memory)

	k8s, err := convertToKubernetes(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "2", k8s.Requests["cpu"])
	assert.Empty(t, k8s.Requests["memory"])
}

func TestCPUOnly_OnlyMemory(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	memory := float64(16)
	spec := &v1.ResourceSpec{
		Memory: &memory,
	}

	ray, err := convertToRay(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumCPUs)
	assert.Equal(t, float64(16*plugin.BytesPerGiB), ray.Memory)

	k8s, err := convertToKubernetes(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "16Gi", k8s.Requests["memory"])
	assert.Empty(t, k8s.Requests["cpu"])
}

func TestGPUZero_NoAcceleratorType(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	gpu := float64(0)
	cpu := float64(4)
	memory := float64(8)
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}

	ray, err := convertToRay(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(4), ray.NumCPUs)
	assert.Equal(t, float64(8*plugin.BytesPerGiB), ray.Memory)

	k8s, err := convertToKubernetes(mgr, spec)
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "4", k8s.Requests["cpu"])
	assert.Equal(t, "8Gi", k8s.Requests["memory"])
}

func TestNoConverterFound(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	mgr.On("GetConverter", "unknown_gpu").Return(nil, false)

	gpu := float64(1)
	spec := &v1.ResourceSpec{
		GPU: &gpu,
	}
	spec.SetAcceleratorType("unknown_gpu")

	_, err := convertToRay(mgr, spec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no converter found")

	_, err = convertToKubernetes(mgr, spec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no converter found")
}

func TestGetDeployedModelRealVersion_BentoML(t *testing.T) {
	tests := []struct {
		name         string
		setupMocks   func(modelregistry *modelregistrymocks.MockModelRegistry)
		inputVersion string
		expected     string
		wantErr      bool
	}{
		{
			name: "bentoml registry model found with real version",
			setupMocks: func(modelregistry *modelregistrymocks.MockModelRegistry) {
				modelregistry.On("Connect").Return(nil)
				modelregistry.On("GetModelVersion", "test", "latest").Return(&v1.ModelVersion{
					Name: "v1.0.0",
				}, nil)
				modelregistry.On("Disconnect").Return(nil)
			},
			inputVersion: "latest",
			expected:     "v1.0.0",
		},
		{
			name: "bentoml registry model found with real version with empty version",
			setupMocks: func(modelregistry *modelregistrymocks.MockModelRegistry) {
				modelregistry.On("Connect").Return(nil)
				modelregistry.On("GetModelVersion", "test", "").Return(&v1.ModelVersion{
					Name: "v1.0.0",
				}, nil)
				modelregistry.On("Disconnect").Return(nil)
			},
			inputVersion: "",
			expected:     "v1.0.0",
		},
		{
			name: "bentoml registry model not found error",
			setupMocks: func(modelregistry *modelregistrymocks.MockModelRegistry) {
				modelregistry.On("Connect").Return(nil)
				modelregistry.On("GetModelVersion", "test", "latest").Return(nil, assert.AnError)
				modelregistry.On("Disconnect").Return(nil)
			},
			inputVersion: "latest",
			wantErr:      true,
		},
		{
			name: "bentoml registry model found with specific version",
			setupMocks: func(modelregistry *modelregistrymocks.MockModelRegistry) {
				// No calls expected since specific version is provided
			},
			inputVersion: "v2.0.0",
			expected:     "v2.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockModelRegistry := &modelregistrymocks.MockModelRegistry{}
			tt.setupMocks(mockModelRegistry)
			model_registry.NewModelRegistry = func(registry *v1.ModelRegistry) (model_registry.ModelRegistry, error) {
				return mockModelRegistry, nil
			}

			result, err := getDeployedModelRealVersion(&v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.BentoMLModelRegistryType,
				},
			}, "test", tt.inputVersion)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}

			mockModelRegistry.AssertExpectations(t)
		})
	}
}

func TestGetDeployedModelRealVersion_Huggingface(t *testing.T) {
	tests := []struct {
		name         string
		inputVersion string
		expected     string
		wantErr      bool
	}{
		{
			name:         "huggingface registry model with real version",
			inputVersion: "latest",
			expected:     "latest",
		},
		{
			name:         "huggingface registry model with empty version",
			inputVersion: "",
			expected:     "main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := getDeployedModelRealVersion(&v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
				},
			}, "test", tt.inputVersion)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestGetDeployedModelRealVersion_ModelRegistry(t *testing.T) {
	test := []struct {
		name          string
		modelRegistry *v1.ModelRegistry
		expectedErr   string
	}{
		{
			name: "unsupported model registry type",
			modelRegistry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: "unsupported_type",
				},
			},
			expectedErr: "unsupported model registry type: unsupported_type",
		},
		{
			name:          "nil model registry",
			modelRegistry: nil,
			expectedErr:   "model registry cannot be nil",
		},
		{
			name: "nil model registry spec",
			modelRegistry: &v1.ModelRegistry{
				Spec: nil,
			},
			expectedErr: "model registry spec cannot be nil",
		},
	}

	for _, tt := range test {
		t.Run(tt.name, func(t *testing.T) {
			_, err := getDeployedModelRealVersion(tt.modelRegistry, "test", "latest")
			if err == nil {
				t.Fatalf("expected error but got nil")
			}
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}
