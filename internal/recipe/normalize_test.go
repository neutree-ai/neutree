package recipe

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestNormalizeModelCatalogSpec(t *testing.T) {
	cpu := "4"

	cases := []struct {
		name           string
		in             *v1.ModelCatalogSpec
		wantVariants   []string
		wantBaseArgsK  []string
		wantBaseEnvK   []string
		wantDefaultMdl string
	}{
		{
			name:         "nil spec yields synthetic default variant",
			in:           nil,
			wantVariants: []string{DefaultVariantName},
		},
		{
			name: "trivial MC lifted to default variant",
			in: &v1.ModelCatalogSpec{
				Model:     &v1.ModelSpec{Name: "qwen3-7b"},
				Resources: &v1.ResourceSpec{CPU: &cpu},
				Variables: map[string]interface{}{
					"engine_args": map[string]interface{}{"tp": 1},
				},
				Env: map[string]string{"FOO": "bar"},
			},
			wantVariants:   []string{DefaultVariantName},
			wantBaseArgsK:  []string{"tp"},
			wantBaseEnvK:   []string{"FOO"},
			wantDefaultMdl: "qwen3-7b",
		},
		{
			name: "recipe MC passes through",
			in: &v1.ModelCatalogSpec{
				Base: &v1.RecipeBase{EngineArgs: map[string]interface{}{"x": 1}},
				Variants: map[string]v1.RecipeVariant{
					"fp8": {Model: &v1.ModelSpec{Name: "fp8-ckpt"}},
				},
			},
			wantVariants:  []string{"fp8"},
			wantBaseArgsK: []string{"x"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeModelCatalogSpec(tc.in)

			if len(got.Variants) != len(tc.wantVariants) {
				t.Fatalf("variants count: got %d want %d", len(got.Variants), len(tc.wantVariants))
			}

			for _, k := range tc.wantVariants {
				if _, ok := got.Variants[k]; !ok {
					t.Fatalf("missing variant %q", k)
				}
			}

			for _, k := range tc.wantBaseArgsK {
				if _, ok := got.Base.EngineArgs[k]; !ok {
					t.Fatalf("base.engine_args missing %q", k)
				}
			}

			for _, k := range tc.wantBaseEnvK {
				if _, ok := got.Base.Env[k]; !ok {
					t.Fatalf("base.env missing %q", k)
				}
			}

			if tc.wantDefaultMdl != "" {
				dv := got.Variants[DefaultVariantName]
				if dv.Model == nil || dv.Model.Name != tc.wantDefaultMdl {
					t.Fatalf("default variant model: got %+v want %s", dv.Model, tc.wantDefaultMdl)
				}
			}
		})
	}
}
