package engine

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// GetBuiltinEngines returns all built-in engine definitions with images for all supported accelerator types.
func GetBuiltinEngines() ([]*v1.Engine, error) {
	llamaCppDefaultEngineSchema, err := GetLlamaCppDefaultEngineSchema()
	if err != nil {
		return nil, err
	}

	vllmV0_8_5EngineSchema, err := GetVLLMDefaultEngineSchema()
	if err != nil {
		return nil, err
	}

	vllmV0_11_2EngineSchema, err := GetVLLMV0_11_2EngineSchema()
	if err != nil {
		return nil, err
	}

	engines := []*v1.Engine{
		{
			APIVersion: "v1",
			Kind:       "Engine",
			Metadata: &v1.Metadata{
				Name: "llama-cpp",
			},
			Spec: &v1.EngineSpec{
				Versions: []*v1.EngineVersion{
					{
						Version:      "v0.3.7",
						ValuesSchema: llamaCppDefaultEngineSchema,
						Images: map[string]*v1.EngineImage{
							"cpu": {
								ImageName: "neutree/llama-cpp-python",
								Tag:       "v0.3.7",
							},
						},
						DeployTemplate: map[string]map[string]string{
							"kubernetes": {
								"default": GetLlamaCppDefaultDeployTemplate(),
							},
						},
					},
				},
				SupportedTasks: []string{v1.TextGenerationModelTask, v1.TextEmbeddingModelTask},
			},
		},
		{
			APIVersion: "v1",
			Kind:       "Engine",
			Metadata: &v1.Metadata{
				Name: "vllm",
			},
			Spec: &v1.EngineSpec{
				Versions: []*v1.EngineVersion{
					{
						Version:      "v0.8.5",
						ValuesSchema: vllmV0_8_5EngineSchema,
					},
					{
						Version:      "v0.11.2",
						ValuesSchema: vllmV0_11_2EngineSchema,
						Images: map[string]*v1.EngineImage{
							"nvidia_gpu": {
								ImageName: "vllm/vllm-openai",
								Tag:       "v0.11.2",
							},
							"amd_gpu": {
								ImageName: "rocm/vllm",
								Tag:       "rocm7.0.0_vllm_0.11.2_20251210",
							},
						},
						DeployTemplate: map[string]map[string]string{
							"kubernetes": {
								"default": GetVLLMDefaultDeployTemplate(),
							},
						},
					},
				},
				SupportedTasks: []string{v1.TextGenerationModelTask, v1.TextEmbeddingModelTask, v1.TextRerankModelTask},
			},
		},
	}

	return engines, nil
}
