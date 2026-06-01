package engine

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
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

			if v.Version == "v0.17.1" {
				if v.Capabilities != nil && v.Capabilities.PD != nil {
					t.Errorf("vllm %s should not advertise PD capability", v.Version)
				}
				if v.HasDeployTemplate(v1.KubernetesClusterType, v1.PDDeployMode) {
					t.Errorf("vllm %s should not register kubernetes/pd deploy template", v.Version)
				}
				if v.Sidecar != nil {
					t.Errorf("vllm %s should not register PD router image config", v.Version)
				}
				if v.HasRayServeEntrypoint(v1.PDDeployMode) {
					t.Errorf("vllm %s should not register ray_serve/pd entrypoint", v.Version)
				}
			}

			switch v.Version {
			case "v0.20.0", "v0.20.0-pdsamehost2026060104":
				if v.Capabilities == nil || v.Capabilities.PD == nil {
					t.Errorf("vllm %s missing PD capability", v.Version)
					continue
				}
				if !v.HasDeployTemplate(v1.KubernetesClusterType, v1.PDDeployMode) {
					t.Errorf("vllm %s missing kubernetes/pd deploy template", v.Version)
				}
				if v.Sidecar == nil || v.Sidecar.Image == nil {
					t.Errorf("vllm %s missing PD router image config", v.Version)
				}
				if !v.HasRayServeEntrypoint(v1.PDDeployMode) {
					t.Errorf("vllm %s missing ray_serve/pd entrypoint", v.Version)
				}
				if got, err := v.GetRayServeEntrypoint(v1.PDDeployMode); err != nil || got == "" {
					t.Errorf("vllm %s invalid ray_serve/pd entrypoint: got %q err=%v", v.Version, got, err)
				}
			}

			if v.Version == "v0.20.0-pdsamehost2026060104" {
				nvidiaImg, ok := v.Images["nvidia_gpu"]
				if !ok {
					t.Errorf("vllm %s missing nvidia_gpu image", v.Version)
				} else if got, want := nvidiaImg.Tag, "v0.20.0-pdsamehost2026060104-ray2.53.0"; got != want {
					t.Errorf("vllm %s nvidia_gpu tag mismatch: got %q, want %q", v.Version, got, want)
				}

				sshImg, ok := v.Images[v1.SSHImageKeyPrefix+"nvidia_gpu"]
				if !ok {
					t.Errorf("vllm %s missing ssh_nvidia_gpu image", v.Version)
				} else if got, want := sshImg.Tag, "v0.20.0-pdsamehost2026060104-ray2.53.0"; got != want {
					t.Errorf("vllm %s ssh_nvidia_gpu tag mismatch: got %q, want %q", v.Version, got, want)
				}

				if v.Sidecar == nil || v.Sidecar.Image == nil {
					t.Errorf("vllm %s missing PD router image config", v.Version)
				} else if got, want := v.Sidecar.Image.Tag, "v0.20.0-pdsamehost2026060104"; got != want {
					t.Errorf("vllm %s sidecar tag mismatch: got %q, want %q", v.Version, got, want)
				}
			}
		}
	}

	// Verify sglang v0.5.10 has nvidia_gpu image, k8s default template, and supported tasks (rerank excluded).
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
		nvidiaImg, ok := v.Images["nvidia_gpu"]
		if !ok {
			t.Errorf("sglang %s missing nvidia_gpu image", v.Version)
		} else {
			// Lock the tag scheme so a Makefile bump or builtin.go drift never
			// silently routes traffic to a stale image.
			if got, want := nvidiaImg.Tag, v.Version+"-ray2.53.0"; got != want {
				t.Errorf("sglang %s nvidia_gpu tag mismatch: got %q, want %q", v.Version, got, want)
			}
			if got, want := nvidiaImg.ImageName, "neutree/engine-sglang"; got != want {
				t.Errorf("sglang %s nvidia_gpu image name mismatch: got %q, want %q", v.Version, got, want)
			}
		}
		if _, ok := v.Images["amd_gpu"]; ok {
			t.Errorf("sglang %s should not register amd_gpu (no AMD test cluster available)", v.Version)
		}
		if k8sTemplates, ok := v.DeployTemplate["kubernetes"]; !ok {
			t.Errorf("sglang %s missing kubernetes deploy template", v.Version)
		} else if _, ok := k8sTemplates["default"]; !ok {
			t.Errorf("sglang %s missing default kubernetes deploy template", v.Version)
		} else if _, ok := k8sTemplates[v1.PDDeployMode]; !ok {
			t.Errorf("sglang %s missing kubernetes/pd deploy template", v.Version)
		}
		if v.Sidecar == nil || v.Sidecar.Image == nil {
			t.Errorf("sglang %s missing PD router image config", v.Version)
		}
		if v.Capabilities == nil || v.Capabilities.PD == nil {
			t.Errorf("sglang %s missing PD capability", v.Version)
		} else {
			if !v.HasRayServeEntrypoint(v1.PDDeployMode) {
				t.Errorf("sglang %s missing ray_serve/pd entrypoint", v.Version)
			}
			got, err := v.GetRayServeEntrypoint(v1.PDDeployMode)
			if err != nil || got != "serve.sglang.v0_5_10.app_pd_collocated:app_builder" {
				t.Errorf("sglang %s ray_serve/pd entrypoint: got %q err=%v", v.Version, got, err)
			}
			wantConnectors := map[string]bool{"nixl": true, "mooncake": true}
			for _, connector := range v.Capabilities.PD.KVConnectors {
				delete(wantConnectors, connector)
			}
			if len(wantConnectors) != 0 {
				t.Errorf("sglang %s missing PD connectors: %v", v.Version, wantConnectors)
			}
		}

		wantTasks := map[string]bool{"text-generation": true, "text-embedding": true}
		for _, task := range e.Spec.SupportedTasks {
			delete(wantTasks, task)
		}
		if len(wantTasks) != 0 {
			t.Errorf("sglang missing supported tasks: %v", wantTasks)
		}

		for _, task := range e.Spec.SupportedTasks {
			if task == v1.TextRerankModelTask {
				t.Errorf("sglang must not advertise text-rerank")
			}
		}
	}
}
