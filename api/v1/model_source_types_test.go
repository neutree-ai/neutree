package v1

import "testing"

func TestValidateExternalEndpointModelSources(t *testing.T) {
	// self-hosted is derived for internal endpoints and must never be storable
	// on an external one -- see the doc comment for why.
	if err := ValidateExternalEndpointModelSources(map[string]string{
		"gpt-4o": ModelSourceThirdPartyPublic,
		"qwen":   ModelSourceSelfHosted,
	}); err == nil {
		t.Fatal("expected self-hosted to be rejected on an external endpoint")
	}

	// Every other value is accepted, including one that is not preset: the
	// source enum stays extensible.
	if err := ValidateExternalEndpointModelSources(map[string]string{
		"a": ModelSourceInternalShared,
		"b": ModelSourceThirdPartyPublic,
		"c": ModelSourcePartner,
		"d": "some-future-source",
		"e": "",
	}); err != nil {
		t.Fatalf("non-self-hosted sources must be accepted, got %v", err)
	}

	// No sources at all is not a violation: the field is optional, and a
	// metadata-only patch carries none.
	if err := ValidateExternalEndpointModelSources(nil); err != nil {
		t.Fatalf("an absent map must be accepted, got %v", err)
	}
}

func TestModelSourceAccessors(t *testing.T) {
	if got := ModelSourceOfEndpoint(); got != ModelSourceSelfHosted {
		t.Fatalf("internal endpoints derive %q, got %q", ModelSourceSelfHosted, got)
	}

	if got := ModelSourceOfExternalEndpoint(nil, "gpt-4o"); got != "" {
		t.Fatalf("expected empty source for a nil endpoint, got %q", got)
	}

	ee := &ExternalEndpoint{Spec: &ExternalEndpointSpec{}}
	if got := ModelSourceOfExternalEndpoint(ee, "gpt-4o"); got != "" {
		t.Fatalf("expected empty source when unset, got %q", got)
	}

	// Sources are per model: two models on one endpoint can differ, which is
	// the whole reason this is not stored on the endpoint.
	ee.Spec.ModelSources = map[string]string{
		"gpt-4o":       ModelSourceThirdPartyPublic,
		"group-shared": ModelSourceInternalShared,
	}

	if got := ModelSourceOfExternalEndpoint(ee, "gpt-4o"); got != ModelSourceThirdPartyPublic {
		t.Fatalf("expected %q, got %q", ModelSourceThirdPartyPublic, got)
	}

	if got := ModelSourceOfExternalEndpoint(ee, "group-shared"); got != ModelSourceInternalShared {
		t.Fatalf("expected %q, got %q", ModelSourceInternalShared, got)
	}

	if got := ModelSourceOfExternalEndpoint(ee, "not-exposed"); got != "" {
		t.Fatalf("expected empty source for an unlisted model, got %q", got)
	}
}
