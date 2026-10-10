package engine

import (
	"testing"
)

func TestGetVLLMV0_17_1EngineSchema(t *testing.T) {
	schema, err := GetVLLMV0_17_1EngineSchema()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected schema to be non-nil")
	}
}

func TestGetVLLMV0_24_0EngineSchema(t *testing.T) {
	schema, err := GetVLLMV0_24_0EngineSchema()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected schema to be non-nil")
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema.properties missing or wrong type")
	}

	for _, key := range []string{
		"device_ids",
		"quantization_config",
		"diffusion_config",
		"fingerprint_mode",
		"fingerprint_value",
		"enable_flash_late_interaction",
		"enable_log_requests",
	} {
		if _, ok := props[key].(map[string]interface{}); !ok {
			t.Errorf("schema missing required vLLM v0.24.0 property %q", key)
		}
	}

	for _, key := range []string{"swap_space", "use_gpu_for_pooling_score", "lora_modules", "log_config_file"} {
		if _, ok := props[key]; ok {
			t.Errorf("schema must not include unsupported or obsolete property %q", key)
		}
	}

	if _, err := GetEngineSchema("vllm-v0.24.0"); err != nil {
		t.Fatalf("EngineSchemas lookup for vLLM v0.24.0 failed: %v", err)
	}
}

func TestGetVLLMV0_31_0EngineSchema(t *testing.T) {
	schema, err := GetVLLMV0_31_0EngineSchema()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected schema to be non-nil")
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema.properties missing or wrong type")
	}

	for _, key := range []string{
		// Carried over from v0.24.0.
		"device_ids",
		"quantization_config",
		"diffusion_config",
		"fingerprint_mode",
		"fingerprint_value",
		"enable_flash_late_interaction",
		"enable_log_requests",
		// FrontendArgs moved to vllm.entrypoints.launchers.cli_args in v0.31.0;
		// a schema generated without that class silently loses these.
		"enable_auto_tool_choice",
		"tool_call_parser",
		"chat_template",
		// New in v0.31.0 and wired through the Ray Serve app.
		"enable_per_request_metrics",
		"tool_strict_level",
		"trust_request_mm_kwargs",
		// New in v0.31.0 engine args.
		"max_num_queued_reqs",
		"max_num_queued_tokens",
	} {
		if _, ok := props[key].(map[string]interface{}); !ok {
			t.Errorf("schema missing required vLLM v0.31.0 property %q", key)
		}
	}

	for _, key := range []string{
		"swap_space",
		"use_gpu_for_pooling_score",
		"lora_modules",
		"log_config_file",
		// Flags removed upstream between v0.24.0 and v0.31.0; `vllm serve`
		// rejects them, so they must not be offered on the Kubernetes path.
		"calculate_kv_scales",
		"max_long_partial_prefills",
		"max_num_partial_prefills",
		"override_attention_dtype",
		// HTTP server settings owned by Neutree, not by the user.
		"host",
		"port",
		"api_key",
		"sse_keep_alive_interval",
		"enable_scale_out",
		// Engine-internal fields with no `vllm serve` flag; the Kubernetes template
		// would render them into a command line vLLM rejects.
		"model_weights",
		"enable_mm_processor_stats",
		"mamba_config",
	} {
		if _, ok := props[key]; ok {
			t.Errorf("schema must not include unsupported or obsolete property %q", key)
		}
	}

	if _, err := GetEngineSchema("vllm-v0.31.0"); err != nil {
		t.Fatalf("EngineSchemas lookup for vLLM v0.31.0 failed: %v", err)
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
	// schema drift early. Field names use underscore form (matches
	// ServerArgs Python kwargs verbatim — SSH/Ray path); the K8s deploy
	// template applies sprig replace "_" "-" at render time so SGLang's
	// kebab-only argparse on the CLI side gets the right flag names.
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema.properties missing or wrong type")
	}
	for _, key := range []string{
		"tp_size", "mem_fraction_static", "dtype", "is_embedding",
		"attention_backend", "cuda_graph_max_bs", "preferred_sampling_params",
		"json_model_override_args", "tool_call_parser", "served_model_name",
	} {
		if _, ok := props[key].(map[string]interface{}); !ok {
			t.Errorf("schema missing required property %q", key)
		}
	}
}
