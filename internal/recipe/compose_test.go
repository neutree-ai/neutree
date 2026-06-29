package recipe

import (
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// fsel builds an ordered boolean-feature selection from feature names.
func fsel(names ...string) []v1.FeatureSelection {
	out := make([]v1.FeatureSelection, 0, len(names))
	for _, n := range names {
		out = append(out, v1.FeatureSelection{Name: n})
	}

	return out
}

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
		Features: []v1.RecipeFeature{
			{
				Name:       "yarn",
				Default:    true,
				EngineArgs: map[string]interface{}{"max-model-len": 131072, "rope-scaling": "yarn"},
				Env:        map[string]string{"VLLM_ALLOW_LONG_MAX_MODEL_LEN": "1"},
			},
			{
				Name:       "reasoning",
				EngineArgs: map[string]interface{}{"enable-reasoning": true},
			},
			{
				Name:          "short-ctx",
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
		got, err := ComposeEndpointSpec(recipe, "bf16", fsel("yarn"))
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
		got, err := ComposeEndpointSpec(recipe, "bf16", fsel("yarn", "reasoning"))
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
		_, err := ComposeEndpointSpec(recipe, "bf16", fsel("yarn", "short-ctx"))
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
		_, err := ComposeEndpointSpec(recipe, "bf16", fsel("ghost"))
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

func TestComposeEndpointSpecFeatureTypes(t *testing.T) {
	maxLen := 262144.0

	mc := &v1.ModelCatalogSpec{
		Engine:   &v1.EndpointEngineSpec{Engine: "vllm"},
		Variants: map[string]v1.RecipeVariant{"default": {Model: &v1.ModelSpec{Name: "m"}}},
		Features: []v1.RecipeFeature{
			{
				Name: "attention-backend",
				Type: v1.RecipeFeatureTypeSelect,
				Options: map[string]v1.RecipeFeatureOption{
					"flash_attn": {EngineArgs: map[string]any{"attention_backend": "FLASH_ATTN"}},
					"xformers":   {EngineArgs: map[string]any{"attention_backend": "XFORMERS"}},
				},
				DefaultOption: "flash_attn",
			},
			{
				Name: "max-model-len",
				Type: v1.RecipeFeatureTypeInput,
				Input: &v1.RecipeFeatureInput{
					ValueType: "int",
					Default:   "32768",
					Min:       func() *float64 { v := 1.0; return &v }(),
					Max:       &maxLen,
				},
				EngineArgs: map[string]any{"max_model_len": "${value}"},
			},
			{
				Name:       "served-name",
				Type:       v1.RecipeFeatureTypeInput,
				Input:      &v1.RecipeFeatureInput{ValueType: "string"},
				EngineArgs: map[string]any{"served_model_name": "prefix-${value}"},
			},
		},
	}

	t.Run("select merges chosen option", func(t *testing.T) {
		got, err := ComposeEndpointSpec(mc, "default", []v1.FeatureSelection{{Name: "attention-backend", Value: "xformers"}})
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if got.EngineArgs["attention_backend"] != "XFORMERS" {
			t.Fatalf("select option not applied: %+v", got.EngineArgs)
		}
	})

	t.Run("select unknown option rejected", func(t *testing.T) {
		_, err := ComposeEndpointSpec(mc, "default", []v1.FeatureSelection{{Name: "attention-backend", Value: "ghost"}})
		if err == nil || !strings.Contains(err.Error(), "no option") {
			t.Fatalf("expected option error, got %v", err)
		}
	})

	t.Run("input coerces int and substitutes ${value}", func(t *testing.T) {
		got, err := ComposeEndpointSpec(mc, "default", []v1.FeatureSelection{{Name: "max-model-len", Value: "65536"}})
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if got.EngineArgs["max_model_len"] != int64(65536) {
			t.Fatalf("expected int64 65536, got %#v", got.EngineArgs["max_model_len"])
		}
	})

	t.Run("input uses default when value omitted", func(t *testing.T) {
		got, err := ComposeEndpointSpec(mc, "default", []v1.FeatureSelection{{Name: "max-model-len"}})
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if got.EngineArgs["max_model_len"] != int64(32768) {
			t.Fatalf("expected default int64 32768, got %#v", got.EngineArgs["max_model_len"])
		}
	})

	t.Run("input string substitution keeps surrounding text", func(t *testing.T) {
		got, err := ComposeEndpointSpec(mc, "default", []v1.FeatureSelection{{Name: "served-name", Value: "abc"}})
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if got.EngineArgs["served_model_name"] != "prefix-abc" {
			t.Fatalf("expected prefix-abc, got %#v", got.EngineArgs["served_model_name"])
		}
	})

	t.Run("input out-of-range rejected", func(t *testing.T) {
		_, err := ComposeEndpointSpec(mc, "default", []v1.FeatureSelection{{Name: "max-model-len", Value: "999999999"}})
		if err == nil || !strings.Contains(err.Error(), "maximum") {
			t.Fatalf("expected range error, got %v", err)
		}
	})

	t.Run("input non-int rejected", func(t *testing.T) {
		_, err := ComposeEndpointSpec(mc, "default", []v1.FeatureSelection{{Name: "max-model-len", Value: "abc"}})
		if err == nil || !strings.Contains(err.Error(), "not an integer") {
			t.Fatalf("expected int parse error, got %v", err)
		}
	})

	t.Run("duplicate selection rejected", func(t *testing.T) {
		_, err := ComposeEndpointSpec(mc, "default", []v1.FeatureSelection{{Name: "served-name", Value: "a"}, {Name: "served-name", Value: "b"}})
		if err == nil || !strings.Contains(err.Error(), "more than once") {
			t.Fatalf("expected duplicate error, got %v", err)
		}
	})
}
