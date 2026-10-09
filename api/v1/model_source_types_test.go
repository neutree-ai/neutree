package v1

import "testing"

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
		"group-shared": ModelSourceSelfHosted,
	}

	if got := ModelSourceOfExternalEndpoint(ee, "gpt-4o"); got != ModelSourceThirdPartyPublic {
		t.Fatalf("expected %q, got %q", ModelSourceThirdPartyPublic, got)
	}

	if got := ModelSourceOfExternalEndpoint(ee, "group-shared"); got != ModelSourceSelfHosted {
		t.Fatalf("expected %q, got %q", ModelSourceSelfHosted, got)
	}

	if got := ModelSourceOfExternalEndpoint(ee, "not-exposed"); got != "" {
		t.Fatalf("expected empty source for an unlisted model, got %q", got)
	}
}
