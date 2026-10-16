package engine

import (
	"reflect"
	"sort"
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

	// Verify the maintained vLLM support window exactly. New installations
	// must not receive retired versions through the builtin registry.
	for _, e := range engines {
		if e.Metadata.Name != "vllm" {
			continue
		}

		gotVersions := make([]string, 0, len(e.Spec.Versions))
		for _, v := range e.Spec.Versions {
			gotVersions = append(gotVersions, v.Version)
			switch v.Version {
			case "v0.17.1-p2", "v0.24.0-p2", "v0.31.0":
				img, ok := v.Images["nvidia_gpu"]
				if !ok {
					t.Errorf("vllm %s missing nvidia_gpu image", v.Version)
					continue
				}
				if img == nil {
					t.Errorf("vllm %s nvidia_gpu image config is nil", v.Version)
					continue
				}
				if v.Version == "v0.24.0-p2" {
					if got, want := img.ImageName, "neutree/engine-vllm"; got != want {
						t.Errorf("vllm %s nvidia_gpu image name mismatch: got %q, want %q", v.Version, got, want)
					}
					if got, want := img.Tag, "v0.24.0-p2-ray2.53.0"; got != want {
						t.Errorf("vllm %s nvidia_gpu tag mismatch: got %q, want %q", v.Version, got, want)
					}
				}
				if v.Version == "v0.31.0" {
					if got, want := img.ImageName, "neutree/engine-vllm"; got != want {
						t.Errorf("vllm %s nvidia_gpu image name mismatch: got %q, want %q", v.Version, got, want)
					}
					if got, want := img.Tag, "v0.31.0-ray2.53.0"; got != want {
						t.Errorf("vllm %s nvidia_gpu tag mismatch: got %q, want %q", v.Version, got, want)
					}
				}
				if k8sTemplates, ok := v.DeployTemplate["kubernetes"]; !ok {
					t.Errorf("vllm %s missing kubernetes deploy template", v.Version)
				} else if _, ok := k8sTemplates["default"]; !ok {
					t.Errorf("vllm %s missing default kubernetes deploy template", v.Version)
				}
			}
		}

		sort.Strings(gotVersions)
		if want := []string{"v0.17.1-p2", "v0.24.0-p2", "v0.31.0"}; !reflect.DeepEqual(gotVersions, want) {
			t.Errorf("vllm builtin versions: got %v, want %v", gotVersions, want)
		}
	}

	if _, err := GetDeployTemplate("vllm-v0.24.0-p2"); err != nil {
		t.Fatalf("DeployTemplates lookup for vLLM v0.24.0 failed: %v", err)
	}

	if _, err := GetDeployTemplate("vllm-v0.31.0"); err != nil {
		t.Fatalf("DeployTemplates lookup for vLLM v0.31.0 failed: %v", err)
	}

	for _, retiredVersion := range []string{"v0.8.5", "v0.11.2"} {
		if _, err := GetEngineSchema("vllm-" + retiredVersion); err == nil {
			t.Errorf("retired vLLM %s schema must not be registered", retiredVersion)
		}
		if _, err := GetDeployTemplate("vllm-" + retiredVersion); err == nil {
			t.Errorf("retired vLLM %s template must not be registered", retiredVersion)
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
		if v.Version != "v0.5.10-p2" {
			t.Errorf("sglang version: got %q, want %q", v.Version, "v0.5.10-p2")
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

func TestPatchedBuiltinEngines(t *testing.T) {
	engines, err := GetBuiltinEngines()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, version, accelerator, image string }{
		{"llama-cpp", "v0.3.7-p2", "cpu", "neutree/engine-llama-cpp"},
		{"llama-cpp", "v0.3.7-p2", "ssh_cpu", "neutree/engine-llama-cpp"},
		{"sglang", "v0.5.10-p2", "nvidia_gpu", "neutree/engine-sglang"},
		{"vllm", "v0.17.1-p2", "nvidia_gpu", "neutree/engine-vllm"},
		{"vllm", "v0.24.0-p2", "nvidia_gpu", "neutree/engine-vllm"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.accelerator, func(t *testing.T) {
			found := false
			for _, e := range engines {
				if e.Metadata.Name != tc.name {
					continue
				}
				for _, v := range e.Spec.Versions {
					if v.Version != tc.version {
						continue
					}
					found = true
					img := v.Images[tc.accelerator]
					if img == nil || img.ImageName != tc.image || img.Tag != tc.version+"-ray2.53.0" {
						t.Errorf("unexpected image: %+v", img)
					}
					schema, err := GetEngineSchema(tc.name + "-" + tc.version)
					if err != nil || !reflect.DeepEqual(schema, v.ValuesSchema) {
						t.Errorf("schema mismatch: %v", err)
					}
					template, err := GetDeployTemplate(tc.name + "-" + tc.version)
					if err != nil || template != v.DeployTemplate["kubernetes"]["default"] {
						t.Errorf("template mismatch: %v", err)
					}
				}
			}
			if !found {
				t.Fatalf("missing builtin %s %s", tc.name, tc.version)
			}
		})
	}
}

func TestUnpatchedEngineMappingsRemoved(t *testing.T) {
	for _, key := range []string{"llama-cpp-v0.3.7", "sglang-v0.5.10", "vllm-v0.17.1", "vllm-v0.24.0"} {
		t.Run(key, func(t *testing.T) {
			if _, err := GetEngineSchema(key); err == nil {
				t.Error("unpatched schema must not be registered")
			}
			if _, err := GetDeployTemplate(key); err == nil {
				t.Error("unpatched template must not be registered")
			}
		})
	}
}
