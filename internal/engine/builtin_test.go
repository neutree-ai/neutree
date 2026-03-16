package engine

import (
	"testing"
)

func TestGetBuiltinEngines(t *testing.T) {
	engines, err := GetBuiltinEngines()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(engines) != 2 {
		t.Fatalf("expected 2 built-in engines, got %d", len(engines))
	}

	engineNames := make(map[string]bool)
	for _, e := range engines {
		engineNames[e.Metadata.Name] = true
	}

	if !engineNames["vllm"] {
		t.Error("expected vllm engine to be registered")
	}

	if !engineNames["llama-cpp"] {
		t.Error("expected llama-cpp engine to be registered")
	}

	// Verify vllm v0.11.2 has nvidia_gpu image
	for _, e := range engines {
		if e.Metadata.Name != "vllm" {
			continue
		}

		for _, v := range e.Spec.Versions {
			if v.Version == "v0.11.2" {
				if _, ok := v.Images["nvidia_gpu"]; !ok {
					t.Error("vllm v0.11.2 missing nvidia_gpu image")
				}
			}
		}
	}
}
