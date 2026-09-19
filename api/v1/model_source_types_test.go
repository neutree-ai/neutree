package v1

import "testing"

func TestValidateExternalEndpointModelSource(t *testing.T) {
	// self-hosted is derived for internal endpoints and must never be storable
	// on an external one -- see the doc comment for why.
	if err := ValidateExternalEndpointModelSource(ModelSourceSelfHosted); err == nil {
		t.Fatal("expected self-hosted to be rejected on an external endpoint")
	}

	// Every other value is accepted, including one that is not preset: the
	// source enum stays extensible.
	for _, source := range []string{
		ModelSourceInternalShared,
		ModelSourceThirdPartyPublic,
		ModelSourcePartner,
		"some-future-source",
		"",
	} {
		if err := ValidateExternalEndpointModelSource(source); err != nil {
			t.Fatalf("source %q must be accepted, got %v", source, err)
		}
	}
}

func TestModelSourceAccessors(t *testing.T) {
	if got := ModelSourceOfEndpoint(); got != ModelSourceSelfHosted {
		t.Fatalf("internal endpoints derive %q, got %q", ModelSourceSelfHosted, got)
	}

	if got := ModelSourceOfExternalEndpoint(nil); got != "" {
		t.Fatalf("expected empty source for a nil endpoint, got %q", got)
	}

	ee := &ExternalEndpoint{}
	if got := ModelSourceOfExternalEndpoint(ee); got != "" {
		t.Fatalf("expected empty source when unset, got %q", got)
	}

	ee.SetLabels(map[string]string{ModelSourceLabel: ModelSourcePartner})

	if got := ModelSourceOfExternalEndpoint(ee); got != ModelSourcePartner {
		t.Fatalf("expected %q, got %q", ModelSourcePartner, got)
	}
}
