package recipe

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// NormalizedRecipe is the "always has variants" view compose works against.
// Trivial MCs (no variants) get synthesized into a single "default" variant
// so the compose path has exactly one shape to handle.
type NormalizedRecipe struct {
	Base     v1.RecipeBase
	Variants map[string]v1.RecipeVariant
	Features map[string]v1.RecipeFeature
	Engine   *v1.EndpointEngineSpec
}

// DefaultVariantName is the synthetic key used when a trivial MC is lifted
// into recipe shape, and also the implicit choice when an endpoint omits
// the variant selector.
const DefaultVariantName = "default"

// NormalizeModelCatalogSpec lifts a (possibly trivial) MC spec into recipe
// shape. For trivial MCs, top-level model/resources become the default
// variant and top-level env / variables.engine_args become the base.
func NormalizeModelCatalogSpec(spec *v1.ModelCatalogSpec) NormalizedRecipe {
	var out NormalizedRecipe

	if spec == nil {
		out.Variants = map[string]v1.RecipeVariant{DefaultVariantName: {}}
		return out
	}

	out.Engine = spec.Engine
	out.Features = spec.Features

	if len(spec.Variants) > 0 {
		out.Variants = spec.Variants

		if spec.Base != nil {
			out.Base = *spec.Base
		}

		return out
	}

	out.Variants = map[string]v1.RecipeVariant{
		DefaultVariantName: {
			Model:     spec.Model,
			Resources: spec.Resources,
		},
	}

	out.Base = v1.RecipeBase{
		EngineArgs: extractEngineArgs(spec.Variables),
		Env:        spec.Env,
	}

	return out
}

// extractEngineArgs pulls a map[string]any "engine_args" out of variables.
// Returns nil when absent or wrong shape — compose treats nil as empty.
func extractEngineArgs(vars map[string]any) map[string]any {
	if vars == nil {
		return nil
	}

	raw, ok := vars["engine_args"]
	if !ok {
		return nil
	}

	args, ok := raw.(map[string]any)
	if !ok {
		return nil
	}

	return args
}
