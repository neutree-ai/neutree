package recipe

import (
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestValidateModelCatalogSpec(t *testing.T) {
	cases := []struct {
		name    string
		in      *v1.ModelCatalogSpec
		wantErr string
	}{
		{name: "nil spec ok", in: nil},
		{
			name: "trivial MC ok",
			in: &v1.ModelCatalogSpec{
				Model: &v1.ModelSpec{Name: "qwen"},
			},
		},
		{
			name: "recipe MC ok",
			in: &v1.ModelCatalogSpec{
				Variants: map[string]v1.RecipeVariant{"a": {}},
				Features: map[string]v1.RecipeFeature{
					"yarn":      {ConflictsWith: []string{"short-ctx"}},
					"short-ctx": {},
				},
			},
		},
		{
			name: "top-level model + variants conflict",
			in: &v1.ModelCatalogSpec{
				Model:    &v1.ModelSpec{Name: "x"},
				Variants: map[string]v1.RecipeVariant{"a": {}},
			},
			wantErr: "cannot set top-level model",
		},
		{
			name: "top-level resources + variants conflict",
			in: &v1.ModelCatalogSpec{
				Resources: &v1.ResourceSpec{},
				Variants:  map[string]v1.RecipeVariant{"a": {}},
			},
			wantErr: "cannot set top-level resources",
		},
		{
			name: "feature conflicts_with unknown",
			in: &v1.ModelCatalogSpec{
				Features: map[string]v1.RecipeFeature{
					"a": {ConflictsWith: []string{"ghost"}},
				},
			},
			wantErr: "unknown feature",
		},
		{
			name: "feature conflicts_with self",
			in: &v1.ModelCatalogSpec{
				Features: map[string]v1.RecipeFeature{
					"a": {ConflictsWith: []string{"a"}},
				},
			},
			wantErr: "lists itself",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModelCatalogSpec(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
