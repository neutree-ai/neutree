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
}

func TestBuilderRejectsForeignControlPlaneBaseline(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

	_, err = builder.BuildReleaseInfo("v1.2.1")
	require.ErrorContains(t, err, "not supported by catalog")
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

func TestInjectCatalogRequiresEquivalentEligibilityAndEarlyUse(t *testing.T) {
	t.Run("accepts equivalent profile material", func(t *testing.T) {
		resetDefaultCatalogForTest(t)

		spec := BuiltinCatalog().Spec()
		profile := profileByName(t, spec.ClusterProfiles, "v1.1.0")
		ssh := profile.Spec.Components[v1.SSHClusterType]
		ssh.NodeAgent.Tag = "enterprise-node-agent"
		profile.Spec.Components[v1.SSHClusterType] = ssh

		catalog, err := NewCatalog(spec)
		require.NoError(t, err)
		require.NoError(t, InjectCatalog(catalog))

		profiles, err := NewBuilder().BuildClusterProfiles("v1.2.0")
		require.NoError(t, err)
		assert.Equal(t, "enterprise-node-agent", profileByName(t, profiles, "v1.1.0").Spec.Components[v1.SSHClusterType].NodeAgent.Tag)
	})

	t.Run("rejects a changed profile set", func(t *testing.T) {
		resetDefaultCatalogForTest(t)

		spec := BuiltinCatalog().Spec()
		spec.ClusterProfiles = []*v1.ClusterProfile{spec.ClusterProfiles[0], spec.ClusterProfiles[2]}
		catalog, err := NewCatalog(spec)
		require.NoError(t, err)
		require.ErrorContains(t, InjectCatalog(catalog), "eligible cluster profile versions")
	})

	t.Run("rejects a changed current release baseline", func(t *testing.T) {
		resetDefaultCatalogForTest(t)

		spec := BuiltinCatalog().Spec()
		spec.CurrentReleaseInfoBaseline = "v1.2.0-rc.1"
		catalog, err := NewCatalog(spec)
		require.NoError(t, err)
		require.ErrorContains(t, InjectCatalog(catalog), "current release baseline")
	})

	t.Run("rejects late and repeated injection", func(t *testing.T) {
		resetDefaultCatalogForTest(t)

		require.NoError(t, InjectCatalog(BuiltinCatalog()))
		require.ErrorContains(t, InjectCatalog(BuiltinCatalog()), "already injected")

		resetDefaultCatalogForTest(t)
		_ = NewBuilder()
		require.ErrorContains(t, InjectCatalog(BuiltinCatalog()), "already consumed")
	})
}

func TestCatalogBoundaryErrors(t *testing.T) {
	_, err := NewCatalog(CatalogSpec{})
	require.Error(t, err)

	_, err = NewBuilderForCatalog(nil)
	require.ErrorContains(t, err, "catalog is required")

	var catalog *Catalog
	assert.Equal(t, CatalogSpec{}, catalog.Spec())

	var builder *catalogBuilder
	assert.Empty(t, builder.CurrentReleaseInfoBaseline())
	_, err = builder.BuildReleaseInfo("v1.2.0")
	require.ErrorContains(t, err, "builder is required")
	_, err = builder.BuildClusterProfiles("v1.2.0")
	require.ErrorContains(t, err, "builder is required")

	resetDefaultCatalogForTest(t)
	require.ErrorContains(t, InjectCatalog(nil), "catalog is required")
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

func resetDefaultCatalogForTest(t *testing.T) {
	t.Helper()

	defaultCatalogState.mu.Lock()
	previousCatalog := defaultCatalogState.catalog
	previousConsumed := defaultCatalogState.consumed
	previousInjected := defaultCatalogState.injected
	defaultCatalogState.catalog = nil
	defaultCatalogState.consumed = false
	defaultCatalogState.injected = false
	defaultCatalogState.mu.Unlock()

	t.Cleanup(func() {
		defaultCatalogState.mu.Lock()
		defaultCatalogState.catalog = previousCatalog
		defaultCatalogState.consumed = previousConsumed
		defaultCatalogState.injected = previousInjected
		defaultCatalogState.mu.Unlock()
	})
}
