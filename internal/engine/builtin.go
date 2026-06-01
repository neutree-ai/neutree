package engine

import (
	"encoding/base64"

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

	vllmV0_20_0EngineSchema, err := GetVLLMV0_20_0EngineSchema()
	if err != nil {
		return nil, err
	}

	sglangV0_5_10EngineSchema, err := GetSGLangV0_5_10EngineSchema()
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
						Version:      "v0.20.0",
						ValuesSchema: vllmV0_20_0EngineSchema,
						Images: map[string]*v1.EngineImage{
							"nvidia_gpu": {
								ImageName: "neutree/engine-vllm",
								Tag:       "v0.20.0-ray2.53.0",
							},
						},
						DeployTemplate: map[string]map[string]string{
							"kubernetes": {
								"default":       GetVLLMV0_20_0DeployTemplate(),
								v1.PDDeployMode: GetVLLMV0_20_0PDDeployTemplate(),
							},
							v1.RayServeDeployTarget: {
								v1.PDDeployMode: rayServeEntrypoint("serve.vllm.v0_20_0.app_pd_collocated:app_builder"),
							},
						},
						Sidecar: pdRouter("v0.20.0"),
						Capabilities: &v1.EngineVersionCapabilities{
							PD: &v1.PDCapabilitySpec{
								KVConnectors:   []string{"nixl", "mooncake"},
								SupportedTasks: []string{v1.TextGenerationModelTask},
							},
						},
					},
					{
						Version:      "v0.20.0-pdsamehost2026060102",
						ValuesSchema: vllmV0_20_0EngineSchema,
						Images: map[string]*v1.EngineImage{
							"nvidia_gpu": {
								ImageName: "neutree/engine-vllm",
								Tag:       "v0.20.0-pdsamehost2026060102-ray2.53.0",
							},
							v1.SSHImageKeyPrefix + "nvidia_gpu": {
								ImageName: "neutree/engine-vllm",
								Tag:       "v0.20.0-pdsamehost2026060102-ray2.53.0",
							},
						},
						DeployTemplate: map[string]map[string]string{
							"kubernetes": {
								"default":       GetVLLMV0_20_0DeployTemplate(),
								v1.PDDeployMode: GetVLLMV0_20_0PDDeployTemplate(),
							},
							v1.RayServeDeployTarget: {
								v1.PDDeployMode: rayServeEntrypoint("serve.vllm.v0_20_0.app_pd_collocated:app_builder"),
							},
						},
						Sidecar:        pdRouter("v0.20.0-pdsamehost2026060102"),
						SupportedTasks: []string{v1.TextGenerationModelTask},
						Capabilities: &v1.EngineVersionCapabilities{
							PD: &v1.PDCapabilitySpec{
								KVConnectors:   []string{"nixl", "mooncake"},
								SupportedTasks: []string{v1.TextGenerationModelTask},
							},
						},
					},
				},
				SupportedTasks: []string{v1.TextGenerationModelTask, v1.TextEmbeddingModelTask, v1.TextRerankModelTask},
			},
		},
		{
			APIVersion: "v1",
			Kind:       "Engine",
			Metadata: &v1.Metadata{
				Name: v1.EngineNameSGLang,
			},
			Spec: &v1.EngineSpec{
				Versions: []*v1.EngineVersion{
					{
						Version:      "v0.5.10",
						ValuesSchema: sglangV0_5_10EngineSchema,
						Images: map[string]*v1.EngineImage{
							"nvidia_gpu": {
								ImageName: "neutree/engine-sglang",
								Tag:       "v0.5.10-ray2.53.0",
							},
						},
						DeployTemplate: map[string]map[string]string{
							"kubernetes": {
								"default":       GetSGLangV0_5_10DeployTemplate(),
								v1.PDDeployMode: GetSGLangV0_5_10PDDeployTemplate(),
							},
							v1.RayServeDeployTarget: {
								v1.PDDeployMode: rayServeEntrypoint("serve.sglang.v0_5_10.app_pd_collocated:app_builder"),
							},
						},
						Sidecar: pdRouter("v0.5.10"),
						Capabilities: &v1.EngineVersionCapabilities{
							PD: &v1.PDCapabilitySpec{
								KVConnectors:   []string{"nixl", "mooncake"},
								SupportedTasks: []string{v1.TextGenerationModelTask},
							},
						},
					},
				},
				// SGLang's /v1/rerank does not match the Cohere/Jina shape
				// Neutree clients expect (bare list with `score`, top_n<=0
				// rejected). Steer users to vLLM for rerank workloads.
				SupportedTasks: []string{v1.TextGenerationModelTask, v1.TextEmbeddingModelTask},
			},
		},
	}

	return engines, nil
}

func rayServeEntrypoint(importPath string) string {
	return base64.StdEncoding.EncodeToString([]byte(importPath))
}

func pdRouter(tag string) *v1.EngineVersionSidecar {
	return &v1.EngineVersionSidecar{
		Image: &v1.EngineImage{
			ImageName: "neutree/pd-router",
			Tag:       tag,
		},
		Port:       8000,
		HealthPath: "/health",
	}
}
