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

	if !engineNames["vllm-omni"] {
		t.Error("expected vllm-omni engine to be registered")
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

	// Phase 1 vllm-omni: SSH cluster mode only.
	// Verify only the SSH-prefixed image is registered and no Kubernetes deploy
	// template is present (K8s mode is deferred to Phase 2).
	for _, e := range engines {
		if e.Metadata.Name != "vllm-omni" {
			continue
		}

		if len(e.Spec.Versions) == 0 {
			t.Fatalf("vllm-omni has no versions registered")
		}

		for _, v := range e.Spec.Versions {
			if v.Version != "v0.18.0" {
				continue
			}
			sshKey := "ssh_nvidia_gpu"
			if _, ok := v.Images[sshKey]; !ok {
				t.Errorf("vllm-omni %s missing %s image", v.Version, sshKey)
			}
			if _, ok := v.Images["nvidia_gpu"]; ok {
				t.Errorf("vllm-omni %s should not register K8s nvidia_gpu image in Phase 1 (SSH only)", v.Version)
			}
			if len(v.DeployTemplate) != 0 {
				t.Errorf("vllm-omni %s should not register Kubernetes deploy template in Phase 1, got %v", v.Version, v.DeployTemplate)
			}
		}
	}
}
