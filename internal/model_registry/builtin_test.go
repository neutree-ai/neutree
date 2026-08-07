package model_registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestBuiltinModelRegistries(t *testing.T) {
	tests := []struct {
		name     string
		config   BuiltinConfig
		wantURL  string
		wantNone bool
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
			name:    "enabled without an endpoint uses the hub",
			config:  BuiltinConfig{Enabled: true},
			wantURL: DefaultHuggingFaceEndpoint,
		},
		{
			name:    "enabled with a mirror",
			config:  BuiltinConfig{Enabled: true, HuggingFaceEndpoint: "https://hf-mirror.example/"},
			wantURL: "https://hf-mirror.example",
		},
		{
			name:    "blank endpoint falls back to the hub",
			config:  BuiltinConfig{Enabled: true, HuggingFaceEndpoint: "   "},
			wantURL: DefaultHuggingFaceEndpoint,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuiltinModelRegistries(tt.config, "default")

			if tt.wantNone {
				assert.Empty(t, got)

				return
			}

			require.Len(t, got, 1)

			registry := got[0]
			assert.Equal(t, BuiltinHuggingFaceRegistryName, registry.Metadata.Name)
			assert.Equal(t, "default", registry.Metadata.Workspace)
			assert.Equal(t, v1.ModelRegistryType(v1.HuggingFaceModelRegistryType), registry.Spec.Type)
			assert.Equal(t, tt.wantURL, registry.Spec.Url)
			// The annotation is what makes withdrawal safe: it is the only thing
			// telling a provisioned registry from one a user created by hand.
			assert.True(t, v1.IsBuiltin(registry.Metadata.Annotations))
			// A public registry is read-only, so it carries no credentials of its
			// own. Anything a user attaches later is theirs.
			assert.Empty(t, registry.Spec.Credentials)
		})
	}
}

func TestBuiltinModelRegistriesArePublic(t *testing.T) {
	// The registries this provisions must be the ones the cache and the storage
	// figures treat as public — one rule, not two.
	got := BuiltinModelRegistries(BuiltinConfig{Enabled: true}, "default")
	require.Len(t, got, 1)

	assert.Equal(t, v1.ModelRegistryVisibilityPublic,
		v1.VisibilityForModelRegistryType(got[0].Spec.Type))
}
