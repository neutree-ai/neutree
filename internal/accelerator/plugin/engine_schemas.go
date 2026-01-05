package plugin

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed schemas/vllm_v0.8.5_engine_schema.json
var vllmDefaultEngineSchema []byte

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
}

// GetEngineSchema returns the schema for a specific engine
func GetEngineSchema(engineName string) (map[string]interface{}, error) {
	schemaFunc, exists := EngineSchemas[engineName]
	if !exists {
		return nil, fmt.Errorf("engine schema not found: %s", engineName)
	}

	return schemaFunc()
}
