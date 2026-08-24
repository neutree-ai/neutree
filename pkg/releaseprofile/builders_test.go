package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestBuilderPreservesPrereleaseControlPlaneIdentity(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

	info, err := builder.BuildReleaseInfo("v1.2.0-alpha.1")
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0-alpha.1", info.GetName())

	profiles, err := builder.BuildClusterProfiles("v1.2.0-alpha.1")
	require.NoError(t, err)
	assert.Len(t, profiles, 3)
}

func TestCatalogAndBuilderReturnDefensiveCopies(t *testing.T) {
	catalog := BuiltinCatalog()
	spec := catalog.Spec()
	profile := profileByName(t, spec.ClusterProfiles, "v1.2.0")
	components, found := profile.Spec.ComponentsFor(v1.SSHClusterType)
	require.True(t, found)
	components.RayRuntime.Tag = "changed-outside-catalog"
	profile.Spec.Components[v1.SSHClusterType] = components

	builder, err := NewBuilderForCatalog(catalog)
	require.NoError(t, err)
	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)
	actual, found := profileByName(t, profiles, "v1.2.0").Spec.ComponentsFor(v1.SSHClusterType)
	require.True(t, found)
	assert.Equal(t, "v1.1.1", actual.RayRuntime.Tag)
}

func TestNewCatalogRejectsInvalidArtifactRules(t *testing.T) {
	spec := BuiltinCatalog().Spec()
	spec.ArtifactRules = append(spec.ArtifactRules, ArtifactRule{
		ClusterType: v1.SSHClusterType,
		Accelerator: "amd_gpu",
		Replacements: []ComponentReplacement{{
			Component: rayRuntimeComponent,
			TagSuffix: "-duplicate",
		}},
	})

	_, err := NewCatalog(spec)
	require.ErrorContains(t, err, "duplicate artifact rule")
}
