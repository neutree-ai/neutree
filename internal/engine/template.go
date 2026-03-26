package engine

import (
	_ "embed"
	"encoding/base64"
	"fmt"
)

//go:embed vllm/v0.11.2/templates/kubernetes/default.yaml
var vllmDefaultDeployTemplate string

//go:embed vllm/v0.17.1/templates/kubernetes/default.yaml
var vllmV0_17_1DeployTemplate string

//go:embed llama-cpp/v0.3.7/templates/kubernetes/default.yaml
var llamaCppDefaultDeployTemplate string

// GetVLLMDefaultDeployTemplate returns the default deployment template for vLLM V0.11.2 engine
func GetVLLMDefaultDeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(vllmDefaultDeployTemplate))
}

// GetVLLMV0_17_1DeployTemplate returns the default deployment template for vLLM V0.17.1 engine
func GetVLLMV0_17_1DeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(vllmV0_17_1DeployTemplate))
}

// GetLlamaCppDefaultDeployTemplate returns the default deployment template for Llama.cpp V0.3.7 engine
func GetLlamaCppDefaultDeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(llamaCppDefaultDeployTemplate))
}

// DeployTemplates contains all available deployment templates
var DeployTemplates = map[string]func() string{
	"vllm-v0.11.2":     GetVLLMDefaultDeployTemplate,
	"vllm-v0.17.1":     GetVLLMV0_17_1DeployTemplate,
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
