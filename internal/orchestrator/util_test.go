package orchestrator

import (
	"fmt"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/pkg/accelerator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stretchr/testify/mock"

	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/model_registry"
	modelregistrymocks "github.com/neutree-ai/neutree/internal/model_registry/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestConverterManager_ConvertToRay_NVIDIA(t *testing.T) {

	mgr := &acceleratormocks.MockManager{}
	mgr.On("GetConverter", "nvidia_gpu").Return(plugin.NewGPUConverter(), true)

	gpu := "2"
	cpu := "16"
	memory := "64"
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(string(v1.AcceleratorTypeNVIDIAGPU))
	spec.SetAcceleratorProduct("NVIDIA-L20")
	spec.AddCustomResource("rdma/hca", "2")

	ray, err := convertToRay(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(2), ray.NumGPUs)
	assert.Equal(t, float64(16), ray.NumCPUs)
	assert.Equal(t, float64(64*plugin.BytesPerGiB), ray.Memory)
	assert.Equal(t, float64(2), ray.Resources["NVIDIA-L20"])
	assert.Equal(t, float64(2), ray.Resources["rdma/hca"])
}

func TestConverterManager_ConvertToRay_Accelerator_ZeroCount(t *testing.T) {

	mgr := &acceleratormocks.MockManager{}
	mgr.On("GetConverter", "nvidia_gpu").Return(plugin.NewGPUConverter(), true)

	gpu := "0"
	cpu := "16"
	memory := "64"
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(string(v1.AcceleratorTypeNVIDIAGPU))
	spec.SetAcceleratorProduct("NVIDIA-L20")
	spec.AddCustomResource("rdma/hca", "2")

	ray, err := convertToRay(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(16), ray.NumCPUs)
	assert.Equal(t, float64(64*plugin.BytesPerGiB), ray.Memory)
	assert.Equal(t, float64(0), ray.Resources["NVIDIA-L20"])
	assert.Equal(t, float64(2), ray.Resources["rdma/hca"])
}

func TestConverterManager_ConvertToKubernetes_NVIDIA(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	mgr.On("GetConverter", "nvidia_gpu").Return(plugin.NewGPUConverter(), true)

	gpu := "1"
	cpu := "8"
	memory := "32"
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(string(v1.AcceleratorTypeNVIDIAGPU))
	spec.SetAcceleratorProduct("NVIDIA-L20")

	k8s, err := convertToKubernetes(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "1", k8s.Requests["nvidia.com/gpu"])
	assert.Equal(t, "1", k8s.Limits["nvidia.com/gpu"])
	assert.Equal(t, "8", k8s.Requests["cpu"])
	assert.Equal(t, "32Gi", k8s.Requests["memory"])
	assert.Equal(t, "NVIDIA-L20", k8s.NodeSelector["nvidia.com/gpu.product"])
}

func TestConverterManager_ConvertToKubernetes_Accelerator_ZeroCount(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	mgr.On("GetConverter", "nvidia_gpu").Return(plugin.NewGPUConverter(), true)

	gpu := "0"
	cpu := "8"
	memory := "32"
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(string(v1.AcceleratorTypeNVIDIAGPU))
	spec.SetAcceleratorProduct("NVIDIA-L20")

	k8s, err := convertToKubernetes(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "", k8s.Requests["nvidia.com/gpu"])
	assert.Equal(t, "", k8s.Limits["nvidia.com/gpu"])
	assert.Equal(t, "8", k8s.Requests["cpu"])
	assert.Equal(t, "32Gi", k8s.Requests["memory"])
	assert.Equal(t, "", k8s.NodeSelector["nvidia.com/gpu.product"])
	assert.Equal(t, "none", k8s.Env["NVIDIA_VISIBLE_DEVICES"])
}

func TestConverterManager_ConvertToRay_AMD(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	mgr.On("GetConverter", "amd_gpu").Return(plugin.NewAMDGPUConverter(), true)

	gpu := "1"
	cpu := "8"
	memory := "32"
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(string(v1.AcceleratorTypeAMDGPU))
	spec.SetAcceleratorProduct("AMD_Instinct_MI300X_VF")

	ray, err := convertToRay(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(1), ray.NumGPUs)
	assert.Equal(t, float64(8), ray.NumCPUs)
	assert.Equal(t, float64(1), ray.Resources["AMD_Instinct_MI300X_VF"])
}

func TestConverterManager_ConvertToKubernetes_AMD(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	mgr.On("GetConverter", "amd_gpu").Return(plugin.NewAMDGPUConverter(), true)

	gpu := "1"
	cpu := "8"
	memory := "32"
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}
	spec.SetAcceleratorType(string(v1.AcceleratorTypeAMDGPU))
	spec.SetAcceleratorProduct("AMD_Instinct_MI300X_VF")
	spec.AddCustomResource("hugepages-2Mi", "1024Mi")

	k8s, err := convertToKubernetes(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "1", k8s.Requests["amd.com/gpu"])
	assert.Equal(t, "1", k8s.Limits["amd.com/gpu"])
	assert.Equal(t, "AMD_Instinct_MI300X_VF", k8s.NodeSelector["amd.com/gpu.product-name"])
	assert.Equal(t, "1024Mi", k8s.Requests["hugepages-2Mi"])
}

func TestConverterManager_CPUOnly(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	cpu := "4"
	memory := "8"
	spec := &v1.ResourceSpec{
		CPU:    &cpu,
		Memory: &memory,
	}

	ray, err := convertToRay(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(4), ray.NumCPUs)
	assert.Equal(t, float64(8*plugin.BytesPerGiB), ray.Memory)

	k8s, err := convertToKubernetes(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "4", k8s.Requests["cpu"])
	assert.Equal(t, "8Gi", k8s.Requests["memory"])
}

func TestCPUOnly_MinimalConfig(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	spec := &v1.ResourceSpec{}

	ray, err := convertToRay(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(0), ray.NumCPUs)
	assert.Equal(t, float64(0), ray.Memory)

	k8s, err := convertToKubernetes(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Empty(t, k8s.Requests)
	assert.Empty(t, k8s.Limits)
}

func TestConverterManager_CPUOnly_OnlyCPU(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	cpu := "2"
	spec := &v1.ResourceSpec{
		CPU: &cpu,
	}

	ray, err := convertToRay(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(2), ray.NumCPUs)
	assert.Equal(t, float64(0), ray.Memory)

	k8s, err := convertToKubernetes(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "2", k8s.Requests["cpu"])
	assert.Empty(t, k8s.Requests["memory"])
}

func TestCPUOnly_OnlyMemory(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	memory := "16"
	spec := &v1.ResourceSpec{
		Memory: &memory,
	}

	ray, err := convertToRay(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumCPUs)
	assert.Equal(t, float64(16*plugin.BytesPerGiB), ray.Memory)

	k8s, err := convertToKubernetes(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "16Gi", k8s.Requests["memory"])
	assert.Empty(t, k8s.Requests["cpu"])
}

func TestParseVLLMHumanReadableInt(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  int64
	}{
		{name: "plain integer string", value: "8589934592", want: 8589934592},
		{name: "binary gigabytes", value: "8G", want: 8 * (1 << 30)},
		{name: "decimal gigabytes", value: "8g", want: 8_000_000_000},
		{name: "fractional decimal gigabytes", value: "1.5g", want: 1_500_000_000},
		{name: "native JSON number", value: float64(8589934592), want: 8589934592},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVLLMHumanReadableInt(tt.value)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseVLLMHumanReadableIntRejectsInvalidValues(t *testing.T) {
	for _, value := range []interface{}{"8GB", "1.5G", "abc", -1, 1.5} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			_, err := parseVLLMHumanReadableInt(value)
			require.Error(t, err)
		})
	}
}

func TestGPUZero_NoAcceleratorType(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	gpu := "0"
	cpu := "4"
	memory := "8"
	spec := &v1.ResourceSpec{
		GPU:    &gpu,
		CPU:    &cpu,
		Memory: &memory,
	}

	ray, err := convertToRay(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, ray)
	assert.Equal(t, float64(0), ray.NumGPUs)
	assert.Equal(t, float64(4), ray.NumCPUs)
	assert.Equal(t, float64(8*plugin.BytesPerGiB), ray.Memory)

	k8s, err := convertToKubernetes(mgr, accelerator.ConvertInput{Spec: spec})
	require.NoError(t, err)
	assert.NotNil(t, k8s)
	assert.Equal(t, "4", k8s.Requests["cpu"])
	assert.Equal(t, "8Gi", k8s.Requests["memory"])
}

func TestNoConverterFound(t *testing.T) {
	mgr := &acceleratormocks.MockManager{}

	mgr.On("GetConverter", "unknown_gpu").Return(nil, false)

	gpu := "1"
	spec := &v1.ResourceSpec{
		GPU: &gpu,
	}
	spec.SetAcceleratorType("unknown_gpu")

	_, err := convertToRay(mgr, accelerator.ConvertInput{Spec: spec})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no converter found")

	_, err = convertToKubernetes(mgr, accelerator.ConvertInput{Spec: spec})
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
				modelregistry.On("GetModelVersion", "test", "latest").Return(&v1.ModelVersion{
					Name: "v1.0.0",
				}, nil)
			},
			inputVersion: "latest",
			expected:     "v1.0.0",
		},
		{
			name: "bentoml registry model found with real version with empty version",
			setupMocks: func(modelregistry *modelregistrymocks.MockModelRegistry) {
				modelregistry.On("GetModelVersion", "test", "").Return(&v1.ModelVersion{
					Name: "v1.0.0",
				}, nil)
			},
			inputVersion: "",
			expected:     "v1.0.0",
		},
		{
			name: "bentoml registry model not found error",
			setupMocks: func(modelregistry *modelregistrymocks.MockModelRegistry) {
				modelregistry.On("GetModelVersion", "test", "latest").Return(nil, assert.AnError)
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
			expected:     "", // Empty version is passed to HuggingFace Hub to use default branch
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

// ModelScope's "version" is a git revision, so there is nothing to look up —
// but unlike Hugging Face, "latest" cannot be passed through. The hub's default
// branch is "master", and it does not refuse a wrong revision: it answers HTTP
// 200 with an empty file list, which a downloader would otherwise be free to
// read as "this model has no files". Normalising to "" makes the downloader omit
// the Revision parameter entirely and let the hub resolve its own default.
func TestGetDeployedModelRealVersion_ModelScope(t *testing.T) {
	tests := []struct {
		name         string
		inputVersion string
		expected     string
	}{
		{
			name:         "latest is normalised away rather than sent as a branch name",
			inputVersion: "latest",
			expected:     "",
		},
		{
			name:         "empty version already means the repository default",
			inputVersion: "",
			expected:     "",
		},
		{
			name:         "an explicit revision is passed through untouched",
			inputVersion: "v1.0.0",
			expected:     "v1.0.0",
		},
		{
			name:         "master is passed through; it is not special-cased here",
			inputVersion: "master",
			expected:     "master",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := getDeployedModelRealVersion(&v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.ModelScopeModelRegistryType,
				},
			}, "Qwen/Qwen3-8B", tt.inputVersion)

			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// The served model id is what a client puts in an OpenAI request. On a hub the
// "version" is a git revision, so appending it would publish the model under a
// name nobody would ask for, and under a different name than the identical model
// deployed from the other hub.
func TestEndpointModelServeName_HubRegistriesDoNotAppendTheRevision(t *testing.T) {
	endpoint := func(version string) *v1.Endpoint {
		return &v1.Endpoint{
			Spec: &v1.EndpointSpec{
				Engine: &v1.EndpointEngineSpec{Engine: v1.EngineNameVLLM},
				Model:  &v1.ModelSpec{Name: "Qwen/Qwen3-8B", Version: version},
			},
		}
	}
	registry := func(kind v1.ModelRegistryType) *v1.ModelRegistry {
		return &v1.ModelRegistry{Spec: &v1.ModelRegistrySpec{Type: kind}}
	}

	for _, kind := range []v1.ModelRegistryType{
		v1.HuggingFaceModelRegistryType,
		v1.ModelScopeModelRegistryType,
	} {
		t.Run(string(kind), func(t *testing.T) {
			assert.Equal(t, "Qwen/Qwen3-8B",
				endpointModelServeName(endpoint("master"), registry(kind)))
		})
	}

	// BentoML is the contrast: there the version names a distinct stored
	// artifact, so it belongs in the served name.
	assert.Equal(t, "Qwen/Qwen3-8B:v3",
		endpointModelServeName(endpoint("v3"), registry(v1.BentoMLModelRegistryType)))
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

// A nil model registry is a normal state, not a lookup failure, so every
// registry-shaped helper in the deploy path has to tolerate it.
func TestModelRegistryOptionalInDeployPath(t *testing.T) {
	t.Run("resolve returns nil, not an error, when no registry is named", func(t *testing.T) {
		store := storagemocks.NewMockStorage(t)

		for _, endpoint := range []*v1.Endpoint{
			{Spec: &v1.EndpointSpec{Model: &v1.ModelSpec{Name: "packaged-ocr"}}},
			{Spec: &v1.EndpointSpec{Model: nil}},
			{Spec: nil},
		} {
			registry, err := resolveEndpointModelRegistry(store, endpoint)

			require.NoError(t, err)
			assert.Nil(t, registry)
		}

		// The store is never consulted for an endpoint that names no registry.
		store.AssertNotCalled(t, "ListModelRegistry", mock.Anything)
	})

	t.Run("registry type renders empty rather than crashing the template", func(t *testing.T) {
		assert.Equal(t, "", endpointModelRegistryType(nil))
		assert.Equal(t, "", endpointModelRegistryType(&v1.ModelRegistry{}))
		assert.Equal(t, string(v1.HuggingFaceModelRegistryType), endpointModelRegistryType(
			&v1.ModelRegistry{Spec: &v1.ModelRegistrySpec{Type: v1.HuggingFaceModelRegistryType}},
		))
	})

	t.Run("serve name falls back to the bare model name", func(t *testing.T) {
		// The version suffix exists to disambiguate versions held by a registry.
		// With no registry there is nothing to disambiguate against, so appending
		// it would invent a name the engine does not serve.
		endpoint := &v1.Endpoint{
			Spec: &v1.EndpointSpec{
				Model:  &v1.ModelSpec{Name: "packaged-ocr", Version: "v2"},
				Engine: &v1.EndpointEngineSpec{Engine: "flex"},
			},
		}

		assert.Equal(t, "packaged-ocr", endpointModelServeName(endpoint, nil))
		assert.Equal(t, "packaged-ocr", endpointModelServeName(endpoint, &v1.ModelRegistry{}))
	})
}
