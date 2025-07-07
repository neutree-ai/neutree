package plugin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetVLLMV1EngineSchema(t *testing.T) {
	schema, err := GetVLLMV1EngineSchema()
	assert.NoError(t, err)
	assert.NotNil(t, schema)

	// Basic schema structure validation
	assert.Equal(t, "object", schema["type"])
	assert.NotNil(t, schema["properties"])
	assert.Equal(t, false, schema["additionalProperties"])

	// Check that properties is a map and not empty
	properties, ok := schema["properties"].(map[string]interface{})
	assert.True(t, ok)
	assert.NotEmpty(t, properties)
}

func TestGetLlamaCppV1EngineSchema(t *testing.T) {
	schema, err := GetLlamaCppV1EngineSchema()
	assert.NoError(t, err)
	assert.NotNil(t, schema)

	// Basic schema structure validation
	assert.Equal(t, "object", schema["type"])
	assert.NotNil(t, schema["properties"])
	assert.Equal(t, false, schema["additionalProperties"])

	// Check that properties is a map and not empty
	properties, ok := schema["properties"].(map[string]interface{})
	assert.True(t, ok)
	assert.NotEmpty(t, properties)
}

func TestGetEngineSchema(t *testing.T) {
	tests := []struct {
		name        string
		engineName  string
		expectError bool
	}{
		{
			name:        "Valid vLLM engine",
			engineName:  "vllm-v1",
			expectError: false,
		},
		{
			name:        "Valid Llama.cpp engine",
			engineName:  "llama-cpp-v1",
			expectError: false,
		},
		{
			name:        "Invalid engine name",
			engineName:  "invalid-engine",
			expectError: true,
		},
		{
			name:        "Empty engine name",
			engineName:  "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema, err := GetEngineSchema(tt.engineName)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, schema)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, schema)

				// Basic validation for valid schemas
				assert.Equal(t, "object", schema["type"])
				assert.NotNil(t, schema["properties"])
			}
		})
	}
}

func TestEngineSchemas(t *testing.T) {
	// Test that all registered engines can be loaded
	for engineName, schemaFunc := range EngineSchemas {
		t.Run(engineName, func(t *testing.T) {
			schema, err := schemaFunc()
			assert.NoError(t, err)
			assert.NotNil(t, schema)

			// Basic schema validation
			assert.Equal(t, "object", schema["type"])
			assert.NotNil(t, schema["properties"])
		})
	}

	// Test that we have the expected engines registered
	assert.Contains(t, EngineSchemas, "vllm-v1")
	assert.Contains(t, EngineSchemas, "llama-cpp-v1")
	assert.Len(t, EngineSchemas, 2)
}
