package v1

// Model source label (NEU-782).
//
// A model's "source" tells users apart models whose cost properties differ
// (a self-hosted model's cost is already sunk; a public cloud one is billed
// per use). It is display / grouping metadata only and MUST NOT take part in
// any quota or access decision — access is still decided by an API key's
// allowed_models, and quotas by ApiKeyLimits.
//
// Granularity: per MODEL. One external endpoint routinely fronts models of
// different origin, and 095's model routes let a single model have targets on
// several upstreams, so neither the endpoint nor the upstream resolves to one
// source per model. The source is an assertion an admin makes about a model.
//
// Storage: ExternalEndpointSpec.ModelSources, keyed by the client-facing model
// name. Deliberately no DB CHECK constraint and no closed Go enum: a new source
// value must not require a code or schema change.
const (
	// ModelSourceSelfHosted covers models this platform serves itself. It is
	// derived for every internal Endpoint, where nothing is stored, and may be
	// set on an ExternalEndpoint model that fronts one.
	ModelSourceSelfHosted = "self-hosted"
	// ModelSourcePrivateAccess covers privately deployed models this platform
	// does not manage, such as ones another department runs with other tooling.
	ModelSourcePrivateAccess = "private-access"
	// ModelSourceThirdPartyPublic covers metered public cloud APIs.
	ModelSourceThirdPartyPublic = "third-party-public"
	// ModelSourceHybrid covers a model routed across sources, such as a
	// self-hosted primary with a public cloud fallback.
	ModelSourceHybrid = "hybrid"
)

// PresetModelSources lists the values the UI offers in its source dropdown.
// It is a suggestion list, not a validation whitelist: an unknown value is
// accepted everywhere so the enum stays extensible.
var PresetModelSources = []string{
	ModelSourceSelfHosted,
	ModelSourcePrivateAccess,
	ModelSourceThirdPartyPublic,
	ModelSourceHybrid,
}

// ModelSourceOfEndpoint returns the derived source of an internal Endpoint.
// Nothing is stored for internal endpoints and admins cannot set it.
func ModelSourceOfEndpoint() string {
	return ModelSourceSelfHosted
}

// ModelSourceOfExternalEndpoint reads the configured source of one model on an
// ExternalEndpoint, or "" when unset. `model` is the client-facing name.
func ModelSourceOfExternalEndpoint(obj *ExternalEndpoint, model string) string {
	if obj == nil || obj.Spec == nil {
		return ""
	}

	return obj.Spec.ModelSources[model]
}
