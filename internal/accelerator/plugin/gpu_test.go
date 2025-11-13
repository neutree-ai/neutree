package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestGPUAcceleratorPlugin_BasicMethods(t *testing.T) {
	plugin := &GPUAcceleratorPlugin{}

	// Test basic interface methods
	assert.Equal(t, string(v1.AcceleratorTypeNVIDIAGPU), plugin.Resource())
	assert.Equal(t, plugin, plugin.Handle())
	assert.Equal(t, InternalPluginType, plugin.Type())
	assert.NoError(t, plugin.Ping(context.Background()))
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
