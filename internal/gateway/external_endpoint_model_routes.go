package gateway

import (
	"fmt"
	"sort"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// compileExternalEndpointModelRoutes resolves the provider references in the
// control-plane model route spec into the self-contained target records the
// gateway plugin needs at request time.
func compileExternalEndpointModelRoutes(ee *v1.ExternalEndpoint, resolved []resolvedUpstream) ([]map[string]interface{}, error) {
	modelRoutes := ee.Spec.ModelRoutes
	if len(modelRoutes) == 0 {
		// Preserve the accepted model_mapping input and its first-available
		// provider order, including models whose providers are all unavailable.
		byModel := make(map[string][]v1.ExternalEndpointModelRouteTarget)

		for i, provider := range resolved {
			for model, upstreamModel := range provider.entry.ModelMapping {
				byModel[model] = append(byModel[model], v1.ExternalEndpointModelRouteTarget{
					Upstream: provider.entry.Name, UpstreamModel: upstreamModel, Priority: i,
				})
			}
		}

		for model, targets := range byModel {
			modelRoutes = append(modelRoutes, v1.ExternalEndpointModelRoute{Model: model, Targets: targets})
		}

		sort.Slice(modelRoutes, func(i, j int) bool { return modelRoutes[i].Model < modelRoutes[j].Model })
	}

	providers := make(map[string]resolvedUpstream, len(resolved))

	for _, provider := range resolved {
		if provider.entry.Name != "" {
			if _, exists := providers[provider.entry.Name]; exists {
				return nil, fmt.Errorf("duplicate upstream name %q", provider.entry.Name)
			}

			providers[provider.entry.Name] = provider
		}
	}

	seenModels := make(map[string]struct{}, len(modelRoutes))
	routes := make([]map[string]interface{}, 0, len(modelRoutes))

	for _, route := range modelRoutes {
		if route.Model == "" {
			return nil, fmt.Errorf("model route model must not be empty")
		}

		if _, exists := seenModels[route.Model]; exists {
			return nil, fmt.Errorf("duplicate model route %q", route.Model)
		}

		seenModels[route.Model] = struct{}{}

		if len(route.Targets) == 0 {
			return nil, fmt.Errorf("model route %q must have at least one target", route.Model)
		}

		if route.MaxAttempts < 0 {
			return nil, fmt.Errorf("model route %q max_attempts must not be negative", route.Model)
		}

		targets := make([]map[string]interface{}, 0, len(route.Targets))

		for _, target := range route.Targets {
			if target.Upstream == "" {
				return nil, fmt.Errorf("model route %q target upstream must not be empty", route.Model)
			}

			provider, ok := providers[target.Upstream]

			if !ok {
				return nil, fmt.Errorf("model route %q references unknown upstream %q", route.Model, target.Upstream)
			}

			if provider.err != nil {
				continue
			}

			if target.UpstreamModel == "" {
				return nil, fmt.Errorf("model route %q target upstream_model must not be empty", route.Model)
			}

			if target.Priority < 0 || target.Weight < 0 || target.MaxInflightRequests < 0 {
				return nil, fmt.Errorf("model route %q target %q has a negative routing value", route.Model, target.Upstream)
			}

			weight := target.Weight

			if weight == 0 {
				weight = 1
			}

			compiled := map[string]interface{}{
				"upstream":              target.Upstream,
				"upstream_model":        target.UpstreamModel,
				"priority":              target.Priority,
				"weight":                weight,
				"max_inflight_requests": target.MaxInflightRequests,
			}

			targets = append(targets, compiled)
		}

		routes = append(routes, map[string]interface{}{
			"model":                route.Model,
			"retryable_conditions": route.RetryableConditions,
			"max_attempts":         route.MaxAttempts,
			"targets":              targets,
		})
	}

	return routes, nil
}
