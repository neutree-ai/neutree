package plugin

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed schemas/vllm_v0.8.5_engine_schema.json
var vllmDefaultEngineSchema []byte

//go:embed schemas/vllm_v0.11.2_engine_schema.json
var vllmV0_11_2EngineSchema []byte

//go:embed schemas/vllm_v0.15.1_engine_schema.json
var vllmV0_15_1EngineSchema []byte

//go:embed schemas/llama_cpp_v0.3.7_engine_schema.json
var llamaCppDefaultEngineSchema []byte

// GetVLLMDefaultEngineSchema returns the parsed JSON schema for vLLM V0.8.5 engine
func GetVLLMDefaultEngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(vllmDefaultEngineSchema, &schema); err != nil {
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

func GetVLLMV0_15_1EngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(vllmV0_15_1EngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse vLLM V0.15.1 engine schema: %w", err)
	}

	return schema, nil
}

// GetLlamaCppDefaultEngineSchema returns the parsed JSON schema for Llama.cpp V0.3.7 engine
func GetLlamaCppDefaultEngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(llamaCppDefaultEngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse Llama.cpp v0.3.7 engine schema: %w", err)
	}

	return schema, nil
}

// EngineSchemas contains all available engine schemas
var EngineSchemas = map[string]func() (map[string]interface{}, error){
	"vllm-v0.8.5":      GetVLLMDefaultEngineSchema,
	"llama-cpp-v0.3.7": GetLlamaCppDefaultEngineSchema,
	"vllm-v0.11.2":     GetVLLMV0_11_2EngineSchema,
	"vllm-v0.15.1":     GetVLLMV0_15_1EngineSchema,
}

// GetEngineSchema returns the schema for a specific engine
func GetEngineSchema(engineName string) (map[string]interface{}, error) {
	schemaFunc, exists := EngineSchemas[engineName]
	if !exists {
		return nil, fmt.Errorf("engine schema not found: %s", engineName)
	}

	return schemaFunc()
}
