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

	// Verify vllm versions have nvidia_gpu image and deploy template
	for _, e := range engines {
		if e.Metadata.Name != "vllm" {
			continue
		}

		for _, v := range e.Spec.Versions {
			switch v.Version {
			case "v0.11.2", "v0.17.1":
				if _, ok := v.Images["nvidia_gpu"]; !ok {
					t.Errorf("vllm %s missing nvidia_gpu image", v.Version)
				}
				if k8sTemplates, ok := v.DeployTemplate["kubernetes"]; !ok {
					t.Errorf("vllm %s missing kubernetes deploy template", v.Version)
				} else if _, ok := k8sTemplates["default"]; !ok {
					t.Errorf("vllm %s missing default kubernetes deploy template", v.Version)
				}
			}
		}
	}
}
