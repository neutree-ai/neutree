package plugin

import (
	_ "embed"
	"encoding/base64"
	"fmt"
)

//go:embed deploy_templates/vllm_v0.8.5_default.yaml
var vllmDefaultDeployTemplate string

//go:embed deploy_templates/llama_cpp_v0.3.6_default.yaml
var llamaCppDefaultDeployTemplate string

// GetVLLMDefaultDeployTemplate returns the default deployment template for vLLM V0.8.5 engine
func GetVLLMDefaultDeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(vllmDefaultDeployTemplate))
}

// GetLlamaCppDefaultDeployTemplate returns the default deployment template for Llama.cpp V0.3.6 engine
func GetLlamaCppDefaultDeployTemplate() string {
	return base64.StdEncoding.EncodeToString([]byte(llamaCppDefaultDeployTemplate))
}

// DeployTemplates contains all available deployment templates
var DeployTemplates = map[string]func() string{
	"vllm-v0.8.5":      GetVLLMDefaultDeployTemplate,
	"llama-cpp-v0.3.6": GetLlamaCppDefaultDeployTemplate,
}

// GetDeployTemplate returns the deployment template for a specific engine
func GetDeployTemplate(engineName string) (string, error) {
	templateFunc, exists := DeployTemplates[engineName]
	if !exists {
		return "", fmt.Errorf("deploy template not found: %s", engineName)
	}

	return templateFunc(), nil
}
