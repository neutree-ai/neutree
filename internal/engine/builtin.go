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

	vllmV0_8_5EngineSchema, err := GetVLLMV0_8_5EngineSchema()
	if err != nil {
		return nil, err
	}

	vllmV0_11_2EngineSchema, err := GetVLLMV0_11_2EngineSchema()
	if err != nil {
		return nil, err
	}

	vllmV0_17_1EngineSchema, err := GetVLLMV0_17_1EngineSchema()
	if err != nil {
		return nil, err
	}

	vllmV0_19_0EngineSchema, err := GetVLLMV0_19_0EngineSchema()
	if err != nil {
		return nil, err
	}

	engines := []*v1.Engine{
		{
			APIVersion: "v1",
			Kind:       "Engine",
			Metadata: &v1.Metadata{
				Name: v1.EngineNameLlamaCpp,
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
							v1.SSHImageKeyPrefix + "cpu": {
								ImageName: "neutree/engine-llama-cpp",
								Tag:       "v0.3.7-ray2.53.0",
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
				Name: v1.EngineNameVLLM,
			},
			Spec: &v1.EngineSpec{
				Versions: []*v1.EngineVersion{
					{
						Version:      "v0.8.5",
						ValuesSchema: vllmV0_8_5EngineSchema,
						Images: map[string]*v1.EngineImage{
							"nvidia_gpu": {
								ImageName: "neutree/engine-vllm",
								Tag:       "v0.8.5-ray2.53.0",
							},
						},
					},
					{
						Version:      "v0.11.2",
						ValuesSchema: vllmV0_11_2EngineSchema,
						Images: map[string]*v1.EngineImage{
							"nvidia_gpu": {
								ImageName: "vllm/vllm-openai",
								Tag:       "v0.11.2",
							},
							v1.SSHImageKeyPrefix + "nvidia_gpu": {
								ImageName: "neutree/engine-vllm",
								Tag:       "v0.11.2-ray2.53.0",
							},
						},
						DeployTemplate: map[string]map[string]string{
							"kubernetes": {
								"default": GetVLLMV0_11_2DeployTemplate(),
							},
						},
					},
					{
						Version:      "v0.17.1",
						ValuesSchema: vllmV0_17_1EngineSchema,
						Images: map[string]*v1.EngineImage{
							"nvidia_gpu": {
								ImageName: "neutree/engine-vllm",
								Tag:       "v0.17.1-ray2.53.0",
							},
						},
						DeployTemplate: map[string]map[string]string{
							"kubernetes": {
								"default": GetVLLMV0_17_1DeployTemplate(),
							},
						},
					},
					{
						Version:      "v0.19.0",
						ValuesSchema: vllmV0_19_0EngineSchema,
						Images: map[string]*v1.EngineImage{
							"nvidia_gpu": {
								ImageName: "neutree/engine-vllm",
								Tag:       "v0.19.0-ray2.53.0",
							},
						},
						DeployTemplate: map[string]map[string]string{
							"kubernetes": {
								"default": GetVLLMV0_19_0DeployTemplate(),
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
