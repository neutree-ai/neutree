package model_registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// byName indexes a provisioning result so a test can assert about one hub
// without depending on the order they come back in.
func byName(registries []*v1.ModelRegistry) map[string]*v1.ModelRegistry {
	indexed := map[string]*v1.ModelRegistry{}
	for _, registry := range registries {
		indexed[registry.Metadata.Name] = registry
	}

	return indexed
}

func TestBuiltinModelRegistries(t *testing.T) {
	tests := []struct {
		name            string
		config          BuiltinConfig
		wantHuggingFace string
		wantModelScope  string
		wantNone        bool
	}{
		{
			// The default. An installation with no route out would otherwise show
			// every user a registry permanently stuck in Failed.
			name:     "disabled provisions nothing",
			config:   BuiltinConfig{},
			wantNone: true,
		},
		{
			name:     "disabled even with an endpoint configured",
			config:   BuiltinConfig{HuggingFaceEndpoint: "https://hf-mirror.example"},
			wantNone: true,
		},
		{
			name:            "enabled without endpoints uses the hubs themselves",
			config:          BuiltinConfig{Enabled: true},
			wantHuggingFace: DefaultHuggingFaceEndpoint,
			wantModelScope:  DefaultModelScopeEndpoint,
		},
		{
			name: "enabled with mirrors",
			config: BuiltinConfig{
				Enabled:             true,
				HuggingFaceEndpoint: "https://hf-mirror.example/",
				ModelScopeEndpoint:  "https://ms-mirror.example/",
			},
			wantHuggingFace: "https://hf-mirror.example",
			wantModelScope:  "https://ms-mirror.example",
		},
		{
			// Mirroring one hub must not silently repoint the other.
			name: "a mirror for one hub leaves the other on its default",
			config: BuiltinConfig{
				Enabled:            true,
				ModelScopeEndpoint: "https://ms-mirror.example",
			},
			wantHuggingFace: DefaultHuggingFaceEndpoint,
			wantModelScope:  "https://ms-mirror.example",
		},
		{
			name:            "blank endpoints fall back to the hubs",
			config:          BuiltinConfig{Enabled: true, HuggingFaceEndpoint: "   ", ModelScopeEndpoint: "  "},
			wantHuggingFace: DefaultHuggingFaceEndpoint,
			wantModelScope:  DefaultModelScopeEndpoint,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuiltinModelRegistries(tt.config, "default")

			if tt.wantNone {
				assert.Empty(t, got)

				return
			}

			require.Len(t, got, 2)
			indexed := byName(got)

			for _, expected := range []struct {
				name     string
				kind     string
				endpoint string
			}{
				{BuiltinHuggingFaceRegistryName, v1.HuggingFaceModelRegistryType, tt.wantHuggingFace},
				{BuiltinModelScopeRegistryName, v1.ModelScopeModelRegistryType, tt.wantModelScope},
			} {
				registry := indexed[expected.name]
				require.NotNil(t, registry, "no built-in registry named %s", expected.name)

				assert.Equal(t, "default", registry.Metadata.Workspace)
				assert.Equal(t, v1.ModelRegistryType(expected.kind), registry.Spec.Type)
				assert.Equal(t, expected.endpoint, registry.Spec.Url)
				// The annotation is what makes withdrawal safe: it is the only thing
				// telling a provisioned registry from one a user created by hand.
				assert.True(t, v1.IsBuiltin(registry.Metadata.Annotations))
				// A public registry is read-only, so it carries no credentials of its
				// own. Anything a user attaches later is theirs.
				assert.Empty(t, registry.Spec.Credentials)
			}
		})
	}
}

func TestBuiltinModelRegistriesArePublic(t *testing.T) {
	// The registries this provisions must be the ones the cache and the storage
	// figures treat as public — one rule, not two. A hub added to the built-in
	// set but not to VisibilityForModelRegistryType would be provisioned into
	// every workspace and then treated as a private store it can never measure.
	got := BuiltinModelRegistries(BuiltinConfig{Enabled: true}, "default")
	require.Len(t, got, 2)

	for _, registry := range got {
		assert.Equal(t, v1.ModelRegistryVisibilityPublic,
			v1.VisibilityForModelRegistryType(registry.Spec.Type),
			"built-in registry %s is not public", registry.Metadata.Name)
	}
}

// The refusal to edit a built-in registry's address names the setting that owns
// it, so every kind that is provisioned must have one to name. A kind added here
// without one would be refused with an explanation that points nowhere.
func TestBuiltinModelRegistriesNameTheirEndpointFlag(t *testing.T) {
	for _, registry := range BuiltinModelRegistries(BuiltinConfig{Enabled: true}, "default") {
		assert.NotEmpty(t, EndpointFlagForModelRegistryType(registry.Spec.Type),
			"built-in registry %s has no configuration setting to name", registry.Metadata.Name)
	}

	// A kind with no built-in form has nothing to point at, and says so rather
	// than naming some other hub's setting.
	assert.Empty(t, EndpointFlagForModelRegistryType(v1.BentoMLModelRegistryType))
}

// Every kind provisioned by default has to be one the factory can build a client
// for, or the registry appears in every workspace and then fails every read.
func TestBuiltinModelRegistriesAreConstructible(t *testing.T) {
	for _, registry := range BuiltinModelRegistries(BuiltinConfig{Enabled: true}, "default") {
		_, err := new(registry)
		assert.NoError(t, err, "built-in registry %s cannot be constructed", registry.Metadata.Name)
	}
}
