package plugin

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed schemas/vllm_v1_engine_schema.json
var vllmV1EngineSchema []byte

//go:embed schemas/llama_cpp_v1_engine_schema.json
var llamaCppV1EngineSchema []byte

// GetVLLMV1EngineSchema returns the parsed JSON schema for vLLM V1 engine
func GetVLLMV1EngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(vllmV1EngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse vLLM V1 engine schema: %w", err)
	}

	return schema, nil
}

// GetLlamaCppV1EngineSchema returns the parsed JSON schema for Llama.cpp V1 engine
func GetLlamaCppV1EngineSchema() (map[string]interface{}, error) {
	var schema map[string]interface{}
	if err := json.Unmarshal(llamaCppV1EngineSchema, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse Llama.cpp V1 engine schema: %w", err)
	}

	return schema, nil
}

// EngineSchemas contains all available engine schemas
var EngineSchemas = map[string]func() (map[string]interface{}, error){
	"vllm-v1":      GetVLLMV1EngineSchema,
	"llama-cpp-v1": GetLlamaCppV1EngineSchema,
}

// GetEngineSchema returns the schema for a specific engine
func GetEngineSchema(engineName string) (map[string]interface{}, error) {
	schemaFunc, exists := EngineSchemas[engineName]
	if !exists {
		return nil, fmt.Errorf("engine schema not found: %s", engineName)
	}

	return schemaFunc()
}
