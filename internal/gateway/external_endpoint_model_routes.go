package gateway

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// compileExternalEndpointModelRoutes resolves the provider references in the
// control-plane model route spec into the self-contained target records the
// gateway plugin needs at request time.
//nolint:wsl // Validation and compilation are intentionally kept together.
func compileExternalEndpointModelRoutes(ee *v1.ExternalEndpoint, ready []resolvedUpstream) ([]map[string]interface{}, error) {
	if ee.Spec == nil || len(ee.Spec.ModelRoutes) == 0 {
		return nil, nil
	}

	providers := make(map[string]resolvedUpstream, len(ready))

	for _, provider := range ready {
		if provider.entry.Name != "" {

			if _, exists := providers[provider.entry.Name]; exists {
				return nil, fmt.Errorf("duplicate upstream name %q", provider.entry.Name)
			}

			providers[provider.entry.Name] = provider
		}
	}

	seenModels := make(map[string]struct{}, len(ee.Spec.ModelRoutes))
	routes := make([]map[string]interface{}, 0, len(ee.Spec.ModelRoutes))

	for _, route := range ee.Spec.ModelRoutes {
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
				"scheme":                provider.scheme,
				"host":                  provider.host,
				"port":                  provider.port,
				"path":                  provider.path,
				"internal":              provider.internal,
				"priority":              target.Priority,
				"weight":                weight,
				"max_inflight_requests": target.MaxInflightRequests,
			}
			if !provider.internal && provider.entry.Auth != nil {
				compiled["auth_header"] = provider.entry.Auth.AuthHeaderValue()
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
