package engine

import (
	"testing"
)

func TestGetVLLMV0_8_5EngineSchema(t *testing.T) {
	schema, err := GetVLLMV0_8_5EngineSchema()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected schema to be non-nil")
	}
}

func TestGetVLLMV0_11_2EngineSchema(t *testing.T) {
	schema, err := GetVLLMV0_11_2EngineSchema()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected schema to be non-nil")
	}
}

func TestGetVLLMV0_17_1EngineSchema(t *testing.T) {
	schema, err := GetVLLMV0_17_1EngineSchema()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected schema to be non-nil")
	}
}

func TestGetLlamaCppDefaultEngineSchema(t *testing.T) {
	schema, err := GetLlamaCppDefaultEngineSchema()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected schema to be non-nil")
	}
}

func TestGetVLLMOmniV0_18_0EngineSchema(t *testing.T) {
	schema, err := GetVLLMOmniV0_18_0EngineSchema()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected schema to be non-nil")
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("expected schema.properties to be an object")
	}
	for _, key := range []string{"omni", "output_modalities", "worker_backend", "ray_address", "deploy_config"} {
		if _, exists := props[key]; !exists {
			t.Errorf("vllm-omni schema missing required property: %s", key)
		}
	}
}
