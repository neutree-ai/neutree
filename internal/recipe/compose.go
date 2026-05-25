package recipe

import (
	"fmt"
	"sort"

	v1 "github.com/neutree-ai/neutree/api/v1"
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

// ComposeEndpointSpec resolves a (variant, enabledFeatures) selection over
// an MC into a concrete kernel. Pure function — same inputs, same outputs;
// no I/O, no clock, no globals.
//
// Merge order (later overrides earlier, top-level keys only — see plan):
//
//	base ← variant ← features (in the order given by enabledFeatures)
func ComposeEndpointSpec(
	mc *v1.ModelCatalogSpec,
	variant string,
	enabledFeatures []string,
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

	if err := validateEnabledFeatures(enabledFeatures, norm.Features); err != nil {
		return nil, err
	}

	engineArgs := map[string]any{}
	env := map[string]string{}

	mergeArgs(engineArgs, norm.Base.EngineArgs)
	mergeEnv(env, norm.Base.Env)

	mergeArgs(engineArgs, v.EngineArgs)
	mergeEnv(env, v.Env)

	for _, name := range enabledFeatures {
		feat := norm.Features[name]
		mergeArgs(engineArgs, feat.EngineArgs)
		mergeEnv(env, feat.Env)
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

func validateEnabledFeatures(enabled []string, features map[string]v1.RecipeFeature) error {
	seen := make(map[string]struct{}, len(enabled))

	for _, name := range enabled {
		if _, dup := seen[name]; dup {
			continue
		}

		seen[name] = struct{}{}

		if _, ok := features[name]; !ok {
			return fmt.Errorf("recipe: unknown feature %q", name)
		}
	}

	for _, a := range enabled {
		feat := features[a]

		for _, conflict := range feat.ConflictsWith {
			if _, on := seen[conflict]; on && conflict != a {
				return fmt.Errorf("recipe: features %q and %q are mutually exclusive", a, conflict)
			}
		}
	}

	return nil
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
