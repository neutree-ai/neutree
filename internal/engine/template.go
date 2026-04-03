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

//go:embed vllm/v0.18.1/templates/kubernetes/default.yaml
var vllmV0_18_1DeployTemplate string

//go:embed vllm/gemma4/templates/kubernetes/default.yaml
var vllmGemma4DeployTemplate string

//go:embed llama-cpp/v0.3.7/templates/kubernetes/default.yaml
var llamaCppDefaultDeployTemplate string

// GetVLLMV0_11_2DeployTemplate returns the default deployment template for vLLM V0.11.2 engine
func GetVLLMV0_11_2DeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(vllmV0_11_2DeployTemplate))
}

// GetVLLMV0_17_1DeployTemplate returns the default deployment template for vLLM V0.17.1 engine
func GetVLLMV0_17_1DeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(vllmV0_17_1DeployTemplate))
}

// GetVLLMV0_18_1DeployTemplate returns the default deployment template for vLLM V0.18.1 engine
func GetVLLMV0_18_1DeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(vllmV0_18_1DeployTemplate))
}

// GetVLLMGemma4DeployTemplate returns the default deployment template for vLLM gemma4 engine
func GetVLLMGemma4DeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(vllmGemma4DeployTemplate))
}

// GetLlamaCppDefaultDeployTemplate returns the default deployment template for Llama.cpp V0.3.7 engine
func GetLlamaCppDefaultDeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(llamaCppDefaultDeployTemplate))
}

// DeployTemplates contains all available deployment templates
var DeployTemplates = map[string]func() string{
	"vllm-v0.11.2":     GetVLLMV0_11_2DeployTemplate,
	"vllm-v0.17.1":     GetVLLMV0_17_1DeployTemplate,
	"vllm-v0.18.1":     GetVLLMV0_18_1DeployTemplate,
	"vllm-gemma4":      GetVLLMGemma4DeployTemplate,
	"llama-cpp-v0.3.7": GetLlamaCppDefaultDeployTemplate,
}

// GetDeployTemplate returns the deployment template for a specific engine
func GetDeployTemplate(engineName string) (string, error) {
	templateFunc, exists := DeployTemplates[engineName]
	if !exists {
		return "", fmt.Errorf("deploy template not found: %s", engineName)
	}

	return templateFunc(), nil
}
