package engine

import (
	"testing"
)

func TestGetBuiltinEngines(t *testing.T) {
	engines, err := GetBuiltinEngines()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(engines) != 3 {
		t.Fatalf("expected 3 built-in engines, got %d", len(engines))
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

	if !engineNames["sglang"] {
		t.Error("expected sglang engine to be registered")
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

	// Verify sglang v0.5.10 has nvidia_gpu image, k8s default template, and three supported tasks.
	for _, e := range engines {
		if e.Metadata.Name != "sglang" {
			continue
		}

		if got, want := len(e.Spec.Versions), 1; got != want {
			t.Errorf("sglang versions: got %d, want %d", got, want)
		}

		v := e.Spec.Versions[0]
		if v.Version != "v0.5.10" {
			t.Errorf("sglang version: got %q, want %q", v.Version, "v0.5.10")
		}
		if _, ok := v.Images["nvidia_gpu"]; !ok {
			t.Errorf("sglang %s missing nvidia_gpu image", v.Version)
		}
		if _, ok := v.Images["amd_gpu"]; ok {
			t.Errorf("sglang %s should not register amd_gpu in v1.1.0 (image deferred until AMD test cluster is available)", v.Version)
		}
		if k8sTemplates, ok := v.DeployTemplate["kubernetes"]; !ok {
			t.Errorf("sglang %s missing kubernetes deploy template", v.Version)
		} else if _, ok := k8sTemplates["default"]; !ok {
			t.Errorf("sglang %s missing default kubernetes deploy template", v.Version)
		}

		wantTasks := map[string]bool{"text-generation": true, "text-embedding": true, "text-rerank": true}
		for _, task := range e.Spec.SupportedTasks {
			delete(wantTasks, task)
		}
		if len(wantTasks) != 0 {
			t.Errorf("sglang missing supported tasks: %v", wantTasks)
		}
	}
}
