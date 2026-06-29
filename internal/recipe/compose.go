package recipe

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// placeholder is the token an input feature's engine_args/env values use to
// mark where the user-supplied value is substituted.
const placeholder = "${value}"

// Recognized input value types (see RecipeFeatureInput.ValueType). The empty
// value defaults to valueTypeString.
const (
	valueTypeString = "string"
	valueTypeInt    = "int"
	valueTypeNumber = "number"
	valueTypeBool   = "bool"
)

// ComposedSpec is the materialized kernel of an endpoint — what the
// controller writes back into the legacy EndpointSpec fields so downstream
// code never has to know about recipes.
type ComposedSpec struct {
	Model      *v1.ModelSpec
	Resources  *v1.ResourceSpec
	Engine     *v1.EndpointEngineSpec
	EngineArgs map[string]any
	Env        map[string]string
}

// ComposeEndpointSpec resolves a (variant, featureSelections) selection over
// an MC into a concrete kernel. Pure function — same inputs, same outputs;
// no I/O, no clock, no globals.
//
// Merge order (later overrides earlier, top-level keys only):
//
//	base ← variant ← features (in the order given by selections)
//
// Each selected feature merges according to its type:
//   - boolean: the feature's engine_args/env.
//   - select:  the feature's engine_args/env, then the chosen option's.
//   - input:   the feature's engine_args/env with "${value}" substituted by
//     the user value (coerced to the input's value_type).
func ComposeEndpointSpec(
	mc *v1.ModelCatalogSpec,
	variant string,
	selections []v1.FeatureSelection,
) (*ComposedSpec, error) {
	if err := ValidateModelCatalogSpec(mc); err != nil {
		return nil, err
	}

	norm := NormalizeModelCatalogSpec(mc)

	if variant == "" {
		variant = DefaultVariantName
	}

	v, ok := norm.Variants[variant]
	if !ok {
		return nil, fmt.Errorf("recipe: unknown variant %q (available: %s)", variant, knownKeys(norm.Variants))
	}

	if err := validateSelections(selections, norm.Features); err != nil {
		return nil, err
	}

	engineArgs := map[string]any{}
	env := map[string]string{}

	mergeArgs(engineArgs, norm.Base.EngineArgs)
	mergeEnv(env, norm.Base.Env)

	mergeArgs(engineArgs, v.EngineArgs)
	mergeEnv(env, v.Env)

	for _, sel := range selections {
		feat := norm.Features[sel.Name]

		switch featureType(feat) {
		case v1.RecipeFeatureTypeSelect:
			opt := feat.Options[sel.Value]
			mergeArgs(engineArgs, feat.EngineArgs)
			mergeEnv(env, feat.Env)
			mergeArgs(engineArgs, opt.EngineArgs)
			mergeEnv(env, opt.Env)
		case v1.RecipeFeatureTypeInput:
			val := inputValue(feat, sel)
			if val == "" && !inputRequired(feat) {
				continue
			}

			mergeArgs(engineArgs, substituteArgs(feat.EngineArgs, val, inputValueType(feat)))
			mergeEnv(env, substituteEnv(feat.Env, val))
		default: // boolean
			mergeArgs(engineArgs, feat.EngineArgs)
			mergeEnv(env, feat.Env)
		}
	}

	out := &ComposedSpec{
		Model:      v.Model,
		Resources:  v.Resources,
		Engine:     norm.Engine,
		EngineArgs: engineArgs,
		Env:        env,
	}

	if len(out.EngineArgs) == 0 {
		out.EngineArgs = nil
	}

	if len(out.Env) == 0 {
		out.Env = nil
	}

	return out, nil
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

// inputValue picks the effective value for an input feature: the user-supplied
// selection value, or the feature's declared default when the user left it blank.
func inputValue(f v1.RecipeFeature, sel v1.FeatureSelection) string {
	if sel.Value != "" {
		return sel.Value
	}

	if f.Input != nil {
		return f.Input.Default
	}

	return ""
}

func validateSelections(selections []v1.FeatureSelection, features map[string]v1.RecipeFeature) error {
	seen := make(map[string]struct{}, len(selections))

	for _, sel := range selections {
		if _, dup := seen[sel.Name]; dup {
			return fmt.Errorf("recipe: feature %q selected more than once", sel.Name)
		}

		seen[sel.Name] = struct{}{}

		feat, ok := features[sel.Name]
		if !ok {
			return fmt.Errorf("recipe: unknown feature %q", sel.Name)
		}

		switch featureType(feat) {
		case v1.RecipeFeatureTypeSelect:
			if sel.Value == "" {
				return fmt.Errorf("recipe: feature %q is select-type and requires an option", sel.Name)
			}

			if _, ok := feat.Options[sel.Value]; !ok {
				return fmt.Errorf("recipe: feature %q has no option %q (available: %s)", sel.Name, sel.Value, optionKeys(feat.Options))
			}
		case v1.RecipeFeatureTypeInput:
			if err := validateInputValue(feat, inputValue(feat, sel)); err != nil {
				return fmt.Errorf("recipe: feature %q: %w", sel.Name, err)
			}
		}
	}

	for _, sel := range selections {
		feat := features[sel.Name]

		for _, conflict := range feat.ConflictsWith {
			if _, on := seen[conflict]; on && conflict != sel.Name {
				return fmt.Errorf("recipe: features %q and %q are mutually exclusive", sel.Name, conflict)
			}
		}
	}

	return nil
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

func substituteArgs(src map[string]any, val, valueType string) map[string]any {
	if len(src) == 0 {
		return nil
	}

	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = substituteValue(v, val, valueType)
	}

	return out
}

// substituteValue replaces the placeholder inside a single engine_args value.
// A value that is exactly "${value}" is coerced to valueType (so numeric
// inputs land as JSON numbers); a value that merely contains the placeholder
// is treated as a string template.
func substituteValue(v any, val, valueType string) any {
	s, ok := v.(string)
	if !ok {
		return v
	}

	if s == placeholder {
		return coerce(val, valueType)
	}

	if strings.Contains(s, placeholder) {
		return strings.ReplaceAll(s, placeholder, val)
	}

	return s
}

func substituteEnv(src map[string]string, val string) map[string]string {
	if len(src) == 0 {
		return nil
	}

	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = strings.ReplaceAll(v, placeholder, val)
	}

	return out
}

func coerce(val, valueType string) any {
	switch valueType {
	case valueTypeInt:
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			return n
		}
	case valueTypeNumber:
		if n, err := strconv.ParseFloat(val, 64); err == nil {
			return n
		}
	case valueTypeBool:
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	}

	return val
}

func contains(list []string, val string) bool {
	for _, x := range list {
		if x == val {
			return true
		}
	}

	return false
}

func optionKeys(m map[string]v1.RecipeFeatureOption) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return strings.Join(keys, ",")
}

func mergeArgs(dst, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}

func mergeEnv(dst, src map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}

func knownKeys(m map[string]v1.RecipeVariant) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	out := ""

	for i, k := range keys {
		if i > 0 {
			out += ","
		}

		out += k
	}

	return out
}
