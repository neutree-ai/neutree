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
		for _, v := range e.Spec.Versions {
			key := e.Metadata.Name + "-" + v.Version
			schema, err := GetEngineSchema(key)
			if err != nil || !reflect.DeepEqual(schema, v.ValuesSchema) {
				t.Errorf("%s schema mismatch: %v", key, err)
			}
			template, err := GetDeployTemplate(key)
			if err != nil || template != v.DeployTemplate["kubernetes"]["default"] {
				t.Errorf("%s template mismatch: %v", key, err)
			}
		}
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
			img := v.Images["nvidia_gpu"]
			if img == nil || img.ImageName != "neutree/engine-vllm" || img.Tag != v.Version+"-ray2.53.0" {
				t.Errorf("vllm %s unexpected nvidia_gpu image: %+v", v.Version, img)
			}
		}

		sort.Strings(gotVersions)
		if want := []string{"v0.17.1-p2", "v0.24.0-p2", "v0.31.0"}; !reflect.DeepEqual(gotVersions, want) {
			t.Errorf("vllm builtin versions: got %v, want %v", gotVersions, want)
		}
	}

	for _, retiredVersion := range []string{"v0.8.5", "v0.11.2"} {
		if _, err := GetEngineSchema("vllm-" + retiredVersion); err == nil {
			t.Errorf("retired vLLM %s schema must not be registered", retiredVersion)
		}
		if _, err := GetDeployTemplate("vllm-" + retiredVersion); err == nil {
			t.Errorf("retired vLLM %s template must not be registered", retiredVersion)
		}
	}

	for _, e := range engines {
		if e.Metadata.Name != "llama-cpp" {
			continue
		}
		if len(e.Spec.Versions) != 1 {
			t.Errorf("llama-cpp: expected one version, got %d", len(e.Spec.Versions))
		}
		for _, v := range e.Spec.Versions {
			if v.Version != "v0.3.7-p2" {
				t.Errorf("llama-cpp version: got %q, want v0.3.7-p2", v.Version)
			}
			for _, accelerator := range []string{"cpu", v1.SSHImageKeyPrefix + "cpu"} {
				img := v.Images[accelerator]
				if img == nil || img.ImageName != "neutree/engine-llama-cpp" || img.Tag != v.Version+"-ray2.53.0" {
					t.Errorf("llama-cpp %s unexpected %s image: %+v", v.Version, accelerator, img)
				}
			}
		}
	}

	// Verify SGLang images and supported tasks (rerank excluded).
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
