package recipe

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ValidateModelCatalogSpec enforces recipe-MC invariants that can be checked
// statically. Trivial MCs (no variants) always pass.
//
//   - Reject mixing top-level model/resources with variants — that combo is
//     ambiguous because compose's normalize step would have to choose which
//     one wins.
//   - Every feature's conflicts_with entry must reference a real feature key
//     so misconfigured catalogs fail at import time, not at compose time.
func ValidateModelCatalogSpec(spec *v1.ModelCatalogSpec) error {
	if spec == nil {
		return nil
	}

	if len(spec.Variants) > 0 {
		if spec.Model != nil {
			return fmt.Errorf("model_catalog: cannot set top-level model together with variants")
		}

		if spec.Resources != nil {
			return fmt.Errorf("model_catalog: cannot set top-level resources together with variants")
		}
	}

	for name, feat := range spec.Features {
		for _, other := range feat.ConflictsWith {
			if other == name {
				return fmt.Errorf("model_catalog: feature %q lists itself in conflicts_with", name)
			}

			if _, ok := spec.Features[other]; !ok {
				return fmt.Errorf("model_catalog: feature %q conflicts_with unknown feature %q", name, other)
			}
		}

		if err := validateFeatureShape(name, feat); err != nil {
			return err
		}
	}

	return nil
}

// validateFeatureShape enforces per-type invariants so misconfigured catalogs
// fail at import time rather than at compose time.
func validateFeatureShape(name string, feat v1.RecipeFeature) error {
	switch featureType(feat) {
	case v1.RecipeFeatureTypeBoolean:
		// nothing extra; Options/Input are simply ignored for boolean.
	case v1.RecipeFeatureTypeSelect:
		if len(feat.Options) == 0 {
			return fmt.Errorf("model_catalog: select feature %q must declare at least one option", name)
		}

		if feat.DefaultOption != "" {
			if _, ok := feat.Options[feat.DefaultOption]; !ok {
				return fmt.Errorf("model_catalog: select feature %q default_option %q is not one of its options", name, feat.DefaultOption)
			}
		}
	case v1.RecipeFeatureTypeInput:
		if feat.Input == nil {
			return fmt.Errorf("model_catalog: input feature %q must declare an input block", name)
		}

		switch inputValueType(feat) {
		case "string", "int", "number", "bool":
		default:
			return fmt.Errorf("model_catalog: input feature %q has unsupported value_type %q", name, feat.Input.ValueType)
		}

		if feat.Input.Min != nil && feat.Input.Max != nil && *feat.Input.Min > *feat.Input.Max {
			return fmt.Errorf("model_catalog: input feature %q has min greater than max", name)
		}

		if feat.Input.Default != "" {
			if err := validateInputValue(feat, feat.Input.Default); err != nil {
				return fmt.Errorf("model_catalog: input feature %q default: %w", name, err)
			}
		}
	default:
		return fmt.Errorf("model_catalog: feature %q has unsupported type %q", name, feat.Type)
	}

	return nil
}
