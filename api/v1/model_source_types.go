package v1

import "fmt"

// Model source label (NEU-782).
//
// A model's "source" tells users apart models whose cost properties differ
// (a self-hosted model's cost is already sunk; a third-party public one is
// billed per use). It is display / grouping metadata only and MUST NOT take
// part in any quota or access decision — access is still decided by an API
// key's allowed_models, and quotas by ApiKeyLimits.
//
// Storage: the generic metadata.labels map under a fixed key, so no schema
// change is needed and the value set stays open. Deliberately no DB CHECK
// constraint and no closed Go enum: a new source value must not require a
// code or schema change.
const (
	// ModelSourceLabel is the label key carrying a model's source on an
	// ExternalEndpoint's metadata.labels.
	ModelSourceLabel = "neutree.ai/model-source"

	// ModelSourceSelfHosted ("自建私有") is derived, never stored: every internal
	// Endpoint is self-hosted by definition, and it is rejected on an
	// ExternalEndpoint (see ValidateExternalEndpointModelSource).
	ModelSourceSelfHosted = "self-hosted"
	// ModelSourceInternalShared is "内部共享".
	ModelSourceInternalShared = "internal-shared"
	// ModelSourceThirdPartyPublic is "第三方公有".
	ModelSourceThirdPartyPublic = "third-party-public"
	// ModelSourcePartner is "合作伙伴".
	ModelSourcePartner = "partner"
)

// PresetModelSources lists the values the UI offers in its source dropdown.
// It is a suggestion list, not a validation whitelist: an unknown value is
// accepted everywhere so the enum stays extensible.
var PresetModelSources = []string{
	ModelSourceSelfHosted,
	ModelSourceInternalShared,
	ModelSourceThirdPartyPublic,
	ModelSourcePartner,
}

// ValidateExternalEndpointModelSource rejects "self-hosted" on an
// ExternalEndpoint. Every other value, known or not, is accepted.
//
// Why self-hosted must stay IE-only: an API key's allowed_models entries are
// (model, type, endpoint_name) triples where type is internal/external (see
// the AllowedModel doc comment in api_key_types.go). The same model name can
// be exposed by both an internal Endpoint and an external one. Once the UI
// replaces the internal/external badge with the source label, the source label
// is the ONLY thing distinguishing the IE row from the EE row for that model
// in the allowed_models picker. Keeping self-hosted <-> IE one-to-one keeps
// that distinction lossless; letting an EE claim self-hosted would make the
// two rows identical and NEU-783's per-model quota unconfigurable in the UI.
//
// This mirrors api.validate_external_endpoint_model_source in
// db/migrations/097_model_source_label.up.sql — the database is the
// authoritative guard, this one only produces a nicer API-boundary error.
func ValidateExternalEndpointModelSource(source string) error {
	if source == ModelSourceSelfHosted {
		return fmt.Errorf("label %s must not be %q on an external endpoint: "+
			"%q is derived for internal endpoints only",
			ModelSourceLabel, ModelSourceSelfHosted, ModelSourceSelfHosted)
	}

	return nil
}

// ModelSourceOfEndpoint returns the derived source of an internal Endpoint.
// Nothing is stored for internal endpoints and admins cannot set it.
func ModelSourceOfEndpoint() string {
	return ModelSourceSelfHosted
}

// ModelSourceOfExternalEndpoint reads the configured source off an
// ExternalEndpoint, or "" when unset.
func ModelSourceOfExternalEndpoint(obj *ExternalEndpoint) string {
	if obj == nil {
		return ""
	}

	return obj.GetLabels()[ModelSourceLabel]
}
