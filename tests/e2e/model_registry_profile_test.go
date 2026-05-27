package e2e

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

func loadModelRegistryProfileForTest(t *testing.T, content string) {
	t.Helper()

	oldProfile := profile
	oldCfg := *Cfg
	Cfg.RunID = "123456"
	t.Cleanup(func() {
		profile = oldProfile
		*Cfg = oldCfg
	})

	profile = Profile{}
	if err := yaml.Unmarshal([]byte(content), &profile); err != nil {
		t.Fatalf("unmarshal profile: %v", err)
	}
}

func TestModelRegistryProfileLegacyDefaultsToBentoML(t *testing.T) {
	loadModelRegistryProfileForTest(t, `
model_registry:
  url: nfs://10.0.0.10:/models
model:
  name: global-model
  version: v1
  task: text-generation
`)

	got := profileModelRegistryForType("")
	if got.Type != "bentoml" {
		t.Fatalf("default model registry type: got %q want bentoml", got.Type)
	}
	if got.URL != "nfs://10.0.0.10:/models" {
		t.Fatalf("default model registry URL: got %q", got.URL)
	}

	if got := testRegistryForType(""); got != "e2e-registry-123456" {
		t.Fatalf("default registry name changed: got %q", got)
	}

	if got := profileModelRegistryTypes(); !reflect.DeepEqual(got, []string{"bentoml"}) {
		t.Fatalf("registry types: got %#v want [bentoml]", got)
	}
}

func TestModelRegistryProfileTypedMapUsesGlobalModel(t *testing.T) {
	loadModelRegistryProfileForTest(t, `
model_registries:
  hugging-face:
    url: https://huggingface.co
  bentoml:
    url: nfs://10.0.0.10:/models
model:
  name: global-model
  version: v1
  task: text-generation
`)

	if got := profileModelRegistryTypes(); !reflect.DeepEqual(got, []string{"bentoml", "hugging-face"}) {
		t.Fatalf("registry types: got %#v", got)
	}

	hf := profileModelRegistryForType("hugging-face")
	if hf.Type != "hugging-face" || hf.URL != "https://huggingface.co" {
		t.Fatalf("hugging-face registry: got %#v", hf)
	}

	if profileModelName() != "global-model" || profileModelVersion() != "v1" || profileModelTask() != "text-generation" {
		t.Fatalf("global model changed: name=%q version=%q task=%q",
			profileModelName(), profileModelVersion(), profileModelTask())
	}

	if got := profileModelRegistryForType("").Type; got != "bentoml" {
		t.Fatalf("implicit default registry type from typed map: got %q", got)
	}
}

func TestModelRegistryProfileRejectsTypeMismatch(t *testing.T) {
	oldProfile := profile
	oldPath, hadPath := os.LookupEnv("E2E_PROFILE_PATH")
	t.Cleanup(func() {
		profile = oldProfile
		if hadPath {
			os.Setenv("E2E_PROFILE_PATH", oldPath)
		} else {
			os.Unsetenv("E2E_PROFILE_PATH")
		}
	})

	path := filepath.Join(t.TempDir(), "profile.yaml")
	if err := os.WriteFile(path, []byte(`
model_registries:
  bentoml:
    type: hugging-face
    url: nfs://10.0.0.10:/models
`), 0644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	os.Setenv("E2E_PROFILE_PATH", path)

	err := LoadProfile()
	if err == nil || !strings.Contains(err.Error(), "model_registries.bentoml.type") {
		t.Fatalf("LoadProfile error: got %v want model registry type mismatch", err)
	}
}

func TestRenderEndpointWithModelRegistryType(t *testing.T) {
	gomega.RegisterTestingT(t)

	loadModelRegistryProfileForTest(t, `
model_registries:
  bentoml:
    url: nfs://10.0.0.10:/models
model:
  name: global-model
  version: v2
  task: text-generation
engines:
  vllm:
    version: v0.11.2
`)

	Cfg.RunID = "777777"
	yamlPath, _ := renderEndpoint(
		"typed-registry-endpoint",
		"cluster-a",
		withModelRegistryType("bentoml"),
		withAccelerator("nvidia_gpu", "NVIDIA-A100"),
	)
	t.Cleanup(func() { os.Remove(yamlPath) })

	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read rendered endpoint: %v", err)
	}
	rendered := string(data)

	for _, want := range []string{
		"registry: e2e-registry-bentoml-777777",
		"name: global-model",
		"version: v2",
		"task: text-generation",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered endpoint missing %q:\n%s", want, rendered)
		}
	}
}

func TestModelRegistryTemplateRendersCredentialsWhenConfigured(t *testing.T) {
	rendered, err := renderTemplate("testdata/model-registry.yaml", map[string]any{
		"E2E_MODEL_REGISTRY":             "registry-with-token",
		"E2E_WORKSPACE":                  "default",
		"E2E_MODEL_REGISTRY_TYPE":        "hugging-face",
		"E2E_MODEL_REGISTRY_URL":         "https://huggingface.co",
		"E2E_MODEL_REGISTRY_CREDENTIALS": "hf_token",
	})
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}

	if !strings.Contains(rendered, "credentials: hf_token") {
		t.Fatalf("rendered model registry should include credentials:\n%s", rendered)
	}
}
