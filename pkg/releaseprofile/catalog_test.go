package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestBuiltinCatalogBuildsCurrentReleaseAndExactProfiles(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

	info, err := builder.BuildReleaseInfo("v1.2.0")
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", info.GetName())
	assert.Equal(t, "v1.2.0", info.Spec.DefaultClusterVersion)
	assert.Equal(t, []string{"v1.1", "v1.2"}, info.Spec.CompatibleClusterBaselines)

	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"v1.1.0", "v1.1.1", "v1.2.0"}, profileNames(profiles))

	profile := profileByName(t, profiles, "v1.2.0")
	assert.Equal(t, v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.1.1"}, profile.Spec.Components[v1.SSHClusterType].RayRuntime)
	assert.Equal(t, v1.ImageRef{Image: "neutree/neutree-runtime", Tag: "v1.1.1"}, profile.Spec.Components[v1.KubernetesClusterType].KubernetesRuntime)
}

func TestBuilderRejectsNonCurrentBaseline(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

	for _, testCase := range []struct {
		name     string
		baseline string
	}{
		{name: "different release", baseline: "v1.2.1"},
		{name: "invalid release", baseline: "not-a-release"},
		{name: "whitespace", baseline: "v1.2.0 "},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := builder.BuildReleaseInfo(testCase.baseline)
			require.ErrorContains(t, err, "not supported by catalog")

			_, err = builder.BuildClusterProfiles(testCase.baseline)
			require.ErrorContains(t, err, "not supported by catalog")
		})
	}
}

func TestCatalogAndBuilderReturnDefensiveCopies(t *testing.T) {
	catalog := BuiltinCatalog()
	spec := catalog.Spec()
	profile := profileByName(t, spec.ClusterProfiles, "v1.2.0")
	ssh := profile.Spec.Components[v1.SSHClusterType]
	ssh.RayRuntime.Tag = "mutated-outside-catalog"
	profile.Spec.Components[v1.SSHClusterType] = ssh

	builder, err := NewBuilderForCatalog(catalog)
	require.NoError(t, err)
	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)

	actual := profileByName(t, profiles, "v1.2.0")
	assert.Equal(t, "v1.1.1", actual.Spec.Components[v1.SSHClusterType].RayRuntime.Tag)
}

func TestBuilderBoundaryErrors(t *testing.T) {
	_, err := NewBuilderForCatalog(nil)
	require.ErrorContains(t, err, "catalog is required")

	var catalog *Catalog
	assert.Equal(t, CatalogSpec{}, catalog.Spec())

	var builder *catalogBuilder
	assert.Empty(t, builder.CurrentReleaseInfoBaseline())
	_, err = builder.BuildReleaseInfo("v1.2.0")
	require.ErrorContains(t, err, "builder is required")
	_, err = builder.BuildClusterProfiles("v1.2.0")
	require.ErrorContains(t, err, "builder is required")
}

func profileNames(profiles []*v1.ClusterProfile) []string {
	names := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		names = append(names, profile.GetName())
	}

	return names
}

func profileByName(t *testing.T, profiles []*v1.ClusterProfile, name string) *v1.ClusterProfile {
	t.Helper()

	for _, profile := range profiles {
		if profile.GetName() == name {
			return profile
		}
	}

	t.Fatalf("cluster profile %q not found", name)

	return nil
}
