package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestGPUAcceleratorPlugin_BasicMethods(t *testing.T) {
	plugin := &GPUAcceleratorPlugin{}

	// Test basic interface methods
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU, plugin.Resource())
	assert.Equal(t, plugin, plugin.Handle())
	assert.Equal(t, InternalPluginType, plugin.Type())
	assert.NoError(t, plugin.Ping(context.Background()))
}

func TestGPUAcceleratorPlugin_GetKubernetesContainerAccelerator(t *testing.T) {
	plugin := &GPUAcceleratorPlugin{}

	tests := []struct {
		name         string
		container    corev1.Container
		expectedGPUs int
	}{
		{
			name: "Container with GPU resources",
			container: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"nvidia.com/gpu": resource.MustParse("2"),
					},
				},
			},
			expectedGPUs: 2,
		},
		{
			name: "Container without GPU resources",
			container: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"cpu": resource.MustParse("1"),
					},
				},
			},
			expectedGPUs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := &v1.GetContainerAcceleratorRequest{
				Container: tt.container,
			}

			response, err := plugin.GetKubernetesContainerAccelerator(context.Background(), request)
			assert.NoError(t, err)
			assert.NotNil(t, response)
			assert.Len(t, response.Accelerators, tt.expectedGPUs)
		})
	}
}

func TestGPUAcceleratorPlugin_GetKubernetesContainerRuntimeConfig(t *testing.T) {
	plugin := &GPUAcceleratorPlugin{}

	tests := []struct {
		name         string
		container    corev1.Container
		expectGPUEnv bool
	}{
		{
			name: "Container with GPU resources",
			container: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"nvidia.com/gpu": resource.MustParse("1"),
					},
				},
			},
			expectGPUEnv: true,
		},
		{
			name: "Container without GPU resources",
			container: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"cpu": resource.MustParse("1"),
					},
				},
			},
			expectGPUEnv: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := &v1.GetContainerRuntimeConfigRequest{
				Container: tt.container,
			}

			response, err := plugin.GetKubernetesContainerRuntimeConfig(context.Background(), request)
			assert.NoError(t, err)
			assert.NotNil(t, response)

			if tt.expectGPUEnv {
				assert.Equal(t, "gpu", response.RuntimeConfig.Env["ACCELERATOR_TYPE"])
			} else {
				assert.Equal(t, "void", response.RuntimeConfig.Env["NVIDIA_VISIBLE_DEVICES"])
			}
		})
	}
}

func TestGPUAcceleratorPlugin_GetSupportEngines(t *testing.T) {
	plugin := &GPUAcceleratorPlugin{}

	response, err := plugin.GetSupportEngines(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, response)
	assert.Len(t, response.Engines, 2)

	// Check that we have expected engines
	engineNames := make(map[string]*v1.Engine)
	for _, engine := range response.Engines {
		engineNames[engine.Metadata.Name] = engine
	}

	// Verify vLLM engine
	vllmEngine, exists := engineNames["vllm"]
	assert.True(t, exists)
	assert.NotNil(t, vllmEngine.Spec.Versions[0].ValuesSchema)
	assert.Contains(t, vllmEngine.Spec.SupportedTasks, v1.TextGenerationModelTask)

	// Verify Llama.cpp engine
	llamaCppEngine, exists := engineNames["llama-cpp"]
	assert.True(t, exists)
	assert.NotNil(t, llamaCppEngine.Spec.Versions[0].ValuesSchema)
	assert.Contains(t, llamaCppEngine.Spec.SupportedTasks, v1.TextGenerationModelTask)
}
