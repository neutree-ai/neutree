package recipe

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Recognized input value types (see RecipeFeatureInput.ValueType). The empty
// value defaults to valueTypeString.
const (
	valueTypeString = "string"
	valueTypeInt    = "int"
	valueTypeNumber = "number"
	valueTypeBool   = "bool"
)

// ValidateModelCatalogSpec enforces recipe-MC invariants that can be checked
// statically, at the point a catalog is written. Trivial MCs (no variants)
// always pass. Composition itself happens client-side; this is the single
// server-side gate that keeps malformed recipe catalogs out of storage.
//
//   - Reject mixing top-level model/resources with variants — that combo is
//     ambiguous about which one wins.
//   - Every feature's conflicts_with entry must reference a real feature key
//     so misconfigured catalogs fail on write, not later at compose time.
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

		// A recipe MC declares its model per-variant (top-level model is
		// forbidden above), so every variant must supply one. Without this a
		// variant would compose to a nil model — a model-less, undeployable
		// endpoint.
		for name, variant := range spec.Variants {
			if variant.Model == nil || strings.TrimSpace(variant.Model.Name) == "" {
				return fmt.Errorf("model_catalog: variant %q must declare a model", name)
			}
		}
	}

	known := make(map[string]struct{}, len(spec.Features))

	for _, feat := range spec.Features {
		if feat.Name == "" {
			return fmt.Errorf("model_catalog: every feature must declare a name")
		}

		if _, dup := known[feat.Name]; dup {
			return fmt.Errorf("model_catalog: duplicate feature name %q", feat.Name)
		}

		known[feat.Name] = struct{}{}
	}

	for _, feat := range spec.Features {
		for _, other := range feat.ConflictsWith {
			if other == feat.Name {
				return fmt.Errorf("model_catalog: feature %q lists itself in conflicts_with", feat.Name)
			}

			if _, ok := known[other]; !ok {
				return fmt.Errorf("model_catalog: feature %q conflicts_with unknown feature %q", feat.Name, other)
			}
		}

		if err := validateFeatureShape(feat.Name, feat); err != nil {
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
		case valueTypeString, valueTypeInt, valueTypeNumber, valueTypeBool:
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

// featureType resolves the effective feature type (empty == boolean).
func featureType(f v1.RecipeFeature) v1.RecipeFeatureType {
	if f.Type == "" {
		return v1.RecipeFeatureTypeBoolean
	}

	return f.Type
}

func inputValueType(f v1.RecipeFeature) string {
	if f.Input == nil || f.Input.ValueType == "" {
		return valueTypeString
	}

	return f.Input.ValueType
}

func inputRequired(f v1.RecipeFeature) bool {
	return f.Input != nil && f.Input.Required
}

// validateInputValue checks a raw input value against the feature's value_type
// and constraints (enum / pattern / min / max). Empty is allowed unless required.
func validateInputValue(f v1.RecipeFeature, val string) error {
	if val == "" {
		if inputRequired(f) {
			return fmt.Errorf("input is required")
		}

		return nil
	}

	if f.Input != nil && len(f.Input.Enum) > 0 && !contains(f.Input.Enum, val) {
		return fmt.Errorf("value %q is not one of the allowed values", val)
	}

	switch inputValueType(f) {
	case valueTypeInt:
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("value %q is not an integer", val)
		}

		return checkRange(f, float64(n))
	case valueTypeNumber:
		n, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("value %q is not a number", val)
		}

		return checkRange(f, n)
	case valueTypeBool:
		if _, err := strconv.ParseBool(val); err != nil {
			return fmt.Errorf("value %q is not a boolean", val)
		}
	default: // string
		if f.Input != nil && f.Input.Pattern != "" {
			ok, err := regexp.MatchString(f.Input.Pattern, val)
			if err != nil {
				return fmt.Errorf("invalid pattern %q: %v", f.Input.Pattern, err)
			}

			if !ok {
				return fmt.Errorf("value %q does not match pattern %q", val, f.Input.Pattern)
			}
		}
	}

	return nil
}

func checkRange(f v1.RecipeFeature, n float64) error {
	if f.Input == nil {
		return nil
	}

	if f.Input.Min != nil && n < *f.Input.Min {
		return fmt.Errorf("value %v is below minimum %v", n, *f.Input.Min)
	}

	if f.Input.Max != nil && n > *f.Input.Max {
		return fmt.Errorf("value %v is above maximum %v", n, *f.Input.Max)
	}

	return nil
}

func contains(list []string, val string) bool {
	for _, x := range list {
		if x == val {
			return true
		}
	}

	return false
}
