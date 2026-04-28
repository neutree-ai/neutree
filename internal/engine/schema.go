package engine

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed vllm/v0.8.5/schema.json
var vllmV0_8_5EngineSchema []byte

//go:embed vllm/v0.11.2/schema.json
var vllmV0_11_2EngineSchema []byte

//go:embed vllm/v0.17.1/schema.json
var vllmV0_17_1EngineSchema []byte

//go:embed vllm/v0.19.0/schema.json
var vllmV0_19_0EngineSchema []byte

//go:embed vllm/v0.19.1/schema.json
var vllmV0_19_1EngineSchema []byte

//go:embed vllm/v0.20.0/schema.json
var vllmV0_20_0EngineSchema []byte

//go:embed vllm/gemma4/schema.json
var vllmGemma4EngineSchema []byte

//go:embed llama-cpp/v0.3.7/schema.json
var llamaCppV0_3_7EngineSchema []byte

// GetVLLMV0_8_5EngineSchema returns the parsed JSON schema for vLLM V0.8.5 engine
func GetVLLMV0_8_5EngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(vllmV0_8_5EngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse vLLM V0.8.5 engine schema: %w", err)
	}

	return schema, nil
}

func GetVLLMV0_11_2EngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(vllmV0_11_2EngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse vLLM V0.11.2 engine schema: %w", err)
	}

	return schema, nil
}

// GetVLLMV0_17_1EngineSchema returns the parsed JSON schema for vLLM V0.17.1 engine
func GetVLLMV0_17_1EngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(vllmV0_17_1EngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse vLLM V0.17.1 engine schema: %w", err)
	}

	return schema, nil
}

// GetLlamaCppDefaultEngineSchema returns the parsed JSON schema for Llama.cpp V0.3.7 engine
func GetLlamaCppDefaultEngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(llamaCppV0_3_7EngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse Llama.cpp v0.3.7 engine schema: %w", err)
	}

	return schema, nil
}

// GetVLLMV0_19_0EngineSchema returns the parsed JSON schema for vLLM V0.19.0 engine
func GetVLLMV0_19_0EngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(vllmV0_19_0EngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse vLLM V0.19.0 engine schema: %w", err)
	}

	return schema, nil
}

// GetVLLMV0_19_1EngineSchema returns the parsed JSON schema for vLLM V0.19.1 engine
func GetVLLMV0_19_1EngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(vllmV0_19_1EngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse vLLM V0.19.1 engine schema: %w", err)
	}

	return schema, nil
}

// GetVLLMV0_20_0EngineSchema returns the parsed JSON schema for vLLM V0.20.0 engine
func GetVLLMV0_20_0EngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(vllmV0_20_0EngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse vLLM V0.20.0 engine schema: %w", err)
	}

	return schema, nil
}

// GetVLLMGemma4EngineSchema returns the parsed JSON schema for vLLM gemma4 engine
func GetVLLMGemma4EngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(vllmGemma4EngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse vLLM gemma4 engine schema: %w", err)
	}

	return schema, nil
}

// EngineSchemas contains all available engine schemas
var EngineSchemas = map[string]func() (map[string]interface{}, error){
	"vllm-v0.8.5":      GetVLLMV0_8_5EngineSchema,
	"llama-cpp-v0.3.7": GetLlamaCppDefaultEngineSchema,
	"vllm-v0.11.2":     GetVLLMV0_11_2EngineSchema,
	"vllm-v0.17.1":     GetVLLMV0_17_1EngineSchema,
	"vllm-v0.19.0":     GetVLLMV0_19_0EngineSchema,
	"vllm-v0.19.1":     GetVLLMV0_19_1EngineSchema,
	"vllm-v0.20.0":     GetVLLMV0_20_0EngineSchema,
	"vllm-gemma4":      GetVLLMGemma4EngineSchema,
}

// GetEngineSchema returns the schema for a specific engine
func GetEngineSchema(engineName string) (map[string]interface{}, error) {
	schemaFunc, exists := EngineSchemas[engineName]
	if !exists {
		return nil, fmt.Errorf("engine schema not found: %s", engineName)
	}

	return schemaFunc()
}
