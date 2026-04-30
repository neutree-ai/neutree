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

func TestGetSGLangV0_5_10EngineSchema(t *testing.T) {
	schema, err := GetSGLangV0_5_10EngineSchema()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected schema to be non-nil")
	}

	// Spot-check a handful of fields documented in the design doc to catch
	// schema drift early. Field names are kebab-case to match SGLang launch_server CLI flags.
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema.properties missing or wrong type")
	}
	for _, key := range []string{
		"tp-size", "mem-fraction-static", "dtype", "is-embedding",
		"attention-backend", "cuda-graph-bs", "preferred-sampling-params",
		"json-model-override-args", "tool-call-parser",
	} {
		if _, ok := props[key].(map[string]interface{}); !ok {
			t.Errorf("schema missing required property %q", key)
		}
	}
}
