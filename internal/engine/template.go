package engine

import (
	_ "embed"
	"encoding/base64"
	"fmt"
)

//go:embed vllm/v0.11.2/templates/kubernetes/default.yaml
var vllmV0_11_2DeployTemplate string

//go:embed vllm/v0.17.1/templates/kubernetes/default.yaml
var vllmV0_17_1DeployTemplate string

//go:embed llama-cpp/v0.3.7/templates/kubernetes/default.yaml
var llamaCppDefaultDeployTemplate string

//go:embed sglang/v0.5.10/templates/kubernetes/default.yaml
var sglangV0_5_10DeployTemplate string

//go:embed sglang/deepseek-v4-hopper/templates/kubernetes/default.yaml
var sglangDeepseekV4HopperDeployTemplate string

// GetVLLMV0_11_2DeployTemplate returns the default deployment template for vLLM V0.11.2 engine
func GetVLLMV0_11_2DeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(vllmV0_11_2DeployTemplate))
}

// GetVLLMV0_17_1DeployTemplate returns the default deployment template for vLLM V0.17.1 engine
func GetVLLMV0_17_1DeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(vllmV0_17_1DeployTemplate))
}

// GetLlamaCppDefaultDeployTemplate returns the default deployment template for Llama.cpp V0.3.7 engine
func GetLlamaCppDefaultDeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(llamaCppDefaultDeployTemplate))
}

// GetSGLangV0_5_10DeployTemplate returns the default deployment template for SGLang V0.5.10 engine
func GetSGLangV0_5_10DeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(sglangV0_5_10DeployTemplate))
}

// GetSGLangDeepseekV4HopperDeployTemplate returns the default deployment template
// for the SGLang DeepSeek-V4 Hopper variant engine.
func GetSGLangDeepseekV4HopperDeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(sglangDeepseekV4HopperDeployTemplate))
}

// DeployTemplates contains all available deployment templates
var DeployTemplates = map[string]func() string{
	"vllm-v0.11.2":     GetVLLMV0_11_2DeployTemplate,
	"vllm-v0.17.1":     GetVLLMV0_17_1DeployTemplate,
	"llama-cpp-v0.3.7": GetLlamaCppDefaultDeployTemplate,
	"sglang-v0.5.10":               GetSGLangV0_5_10DeployTemplate,
	"sglang-deepseek-v4-hopper":    GetSGLangDeepseekV4HopperDeployTemplate,
}

// GetDeployTemplate returns the deployment template for a specific engine
func GetDeployTemplate(engineName string) (string, error) {
	templateFunc, exists := DeployTemplates[engineName]
	if !exists {
		return "", fmt.Errorf("deploy template not found: %s", engineName)
	}

	return templateFunc(), nil
}
