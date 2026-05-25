package recipe

import (
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestComposeEndpointSpec(t *testing.T) {
	cpu := "8"
	gpu := "1"

	trivial := &v1.ModelCatalogSpec{
		Model:     &v1.ModelSpec{Name: "qwen3-7b"},
		Resources: &v1.ResourceSpec{CPU: &cpu},
		Engine:    &v1.EndpointEngineSpec{Engine: "vllm", Version: "0.6.0"},
		Variables: map[string]interface{}{
			"engine_args": map[string]interface{}{"tp": 1, "max-model-len": 4096},
		},
		Env: map[string]string{"FOO": "bar"},
	}

	recipe := &v1.ModelCatalogSpec{
		Engine: &v1.EndpointEngineSpec{Engine: "vllm"},
		Base: &v1.RecipeBase{
			EngineArgs: map[string]interface{}{"tp": 1, "max-model-len": 4096},
			Env:        map[string]string{"FOO": "bar"},
		},
		Variants: map[string]v1.RecipeVariant{
			"bf16": {
				Model:      &v1.ModelSpec{Name: "qwen3-27b-bf16"},
				Resources:  &v1.ResourceSpec{GPU: &gpu},
				EngineArgs: map[string]interface{}{"dtype": "bfloat16"},
			},
			"fp8": {
				Model:      &v1.ModelSpec{Name: "qwen3-27b-fp8"},
				EngineArgs: map[string]interface{}{"quantization": "fp8"},
			},
		},
		Features: map[string]v1.RecipeFeature{
			"yarn": {
				Default:    true,
				EngineArgs: map[string]interface{}{"max-model-len": 131072, "rope-scaling": "yarn"},
				Env:        map[string]string{"VLLM_ALLOW_LONG_MAX_MODEL_LEN": "1"},
			},
			"reasoning": {
				EngineArgs: map[string]interface{}{"enable-reasoning": true},
			},
			"short-ctx": {
				EngineArgs:    map[string]interface{}{"max-model-len": 8192},
				ConflictsWith: []string{"yarn"},
			},
		},
	}

	t.Run("trivial MC composes equal to legacy fields", func(t *testing.T) {
		got, err := ComposeEndpointSpec(trivial, "", nil)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if got.Model == nil || got.Model.Name != "qwen3-7b" {
			t.Fatalf("model: %+v", got.Model)
		}

		if got.Resources == nil || got.Resources.CPU == nil || *got.Resources.CPU != "8" {
			t.Fatalf("resources: %+v", got.Resources)
		}

		if got.Engine == nil || got.Engine.Engine != "vllm" {
			t.Fatalf("engine: %+v", got.Engine)
		}

		if got.EngineArgs["tp"] != 1 || got.EngineArgs["max-model-len"] != 4096 {
			t.Fatalf("engine args: %+v", got.EngineArgs)
		}

		if got.Env["FOO"] != "bar" {
			t.Fatalf("env: %+v", got.Env)
		}
	})

	t.Run("recipe MC empty features = base + variant", func(t *testing.T) {
		got, err := ComposeEndpointSpec(recipe, "bf16", nil)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if got.Model.Name != "qwen3-27b-bf16" {
			t.Fatalf("model: %+v", got.Model)
		}

		if got.EngineArgs["tp"] != 1 {
			t.Fatalf("missing base tp: %+v", got.EngineArgs)
		}

		if got.EngineArgs["dtype"] != "bfloat16" {
			t.Fatalf("missing variant dtype: %+v", got.EngineArgs)
		}
	})

	t.Run("feature overrides variant overrides base", func(t *testing.T) {
		got, err := ComposeEndpointSpec(recipe, "bf16", []string{"yarn"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if got.EngineArgs["max-model-len"] != 131072 {
			t.Fatalf("yarn should override max-model-len: %+v", got.EngineArgs)
		}

		if got.Env["VLLM_ALLOW_LONG_MAX_MODEL_LEN"] != "1" {
			t.Fatalf("yarn env missing: %+v", got.Env)
		}
	})

	t.Run("feature order matters", func(t *testing.T) {
		got, err := ComposeEndpointSpec(recipe, "bf16", []string{"yarn", "reasoning"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if got.EngineArgs["enable-reasoning"] != true {
			t.Fatalf("reasoning not applied: %+v", got.EngineArgs)
		}

		if got.EngineArgs["max-model-len"] != 131072 {
			t.Fatalf("yarn override lost: %+v", got.EngineArgs)
		}
	})

	t.Run("conflicts_with rejected", func(t *testing.T) {
		_, err := ComposeEndpointSpec(recipe, "bf16", []string{"yarn", "short-ctx"})
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("expected conflict error, got %v", err)
		}
	})

	t.Run("unknown variant rejected", func(t *testing.T) {
		_, err := ComposeEndpointSpec(recipe, "ghost", nil)
		if err == nil || !strings.Contains(err.Error(), "unknown variant") {
			t.Fatalf("expected unknown variant error, got %v", err)
		}
	})

	t.Run("unknown feature rejected", func(t *testing.T) {
		_, err := ComposeEndpointSpec(recipe, "bf16", []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), "unknown feature") {
			t.Fatalf("expected unknown feature error, got %v", err)
		}
	})

	t.Run("default variant fallback for trivial when variant omitted", func(t *testing.T) {
		got, err := ComposeEndpointSpec(trivial, "", nil)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if got.Model.Name != "qwen3-7b" {
			t.Fatalf("default variant model: %+v", got.Model)
		}
	})

	t.Run("validate error surfaces via compose", func(t *testing.T) {
		bad := &v1.ModelCatalogSpec{
			Model:    &v1.ModelSpec{Name: "x"},
			Variants: map[string]v1.RecipeVariant{"a": {}},
		}

		_, err := ComposeEndpointSpec(bad, "a", nil)
		if err == nil || !strings.Contains(err.Error(), "top-level model") {
			t.Fatalf("expected validate error, got %v", err)
		}
	})
}
