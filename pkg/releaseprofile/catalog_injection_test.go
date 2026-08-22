package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestBuiltinCatalogBuilderBuildsExactProfilesAndPackageImages(t *testing.T) {
	resetDefaultCatalogForTest(t)

	builder := NewBuilder()
	info, err := builder.BuildReleaseInfo("v1.2.0")
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", info.GetName())
	assert.Equal(t, "v1.2.0", info.Spec.DefaultClusterVersion)
	assert.Equal(t, []string{"v1.1", "v1.2"}, info.Spec.CompatibleClusterBaselines)

	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)
	require.Len(t, profiles, 3)
	assert.ElementsMatch(t, []string{"v1.1.0", "v1.1.1", "v1.2.0"}, profileNames(profiles))

	images, err := builder.BuildPackageImages("v1.2.0", v1.SSHClusterType, "amd_gpu")
	require.NoError(t, err)
	assert.Equal(t, []v1.ImageRef{
		{Image: "neutree/neutree-serve", Tag: "v1.1.1-rocm"},
		{Image: "neutree/neutree-node-agent", Tag: "v1.1.0-rc.1"},
		{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
		{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
	}, images)
}

func TestInjectCatalogAllowsOnlyEquivalentEligibilityBeforeDefaultUse(t *testing.T) {
	resetDefaultCatalogForTest(t)

	spec := BuiltinCatalog().Spec()
	require.NotEmpty(t, spec.ClusterProfiles)

	profile := cloneClusterProfile(spec.ClusterProfiles[0])
	ssh, found := profile.Spec.ComponentsFor(v1.SSHClusterType)
	require.True(t, found)
	ssh.NodeAgent.Tag = "enterprise-node-agent"
	profile.Spec.Components[v1.SSHClusterType] = ssh
	spec.ClusterProfiles[0] = profile
	spec.ArtifactRules = append(spec.ArtifactRules, ArtifactRule{
		ClusterType: v1.SSHClusterType,
		Accelerator: "npu-ascend910b",
		Replacements: []ComponentReplacement{{
			Component: "ray_runtime",
			TagSuffix: "-npu-ascend910b",
		}},
	})

	catalog, err := NewCatalog(spec)
	require.NoError(t, err)
	require.NoError(t, InjectCatalog(catalog))

	builder := NewBuilder()
	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)
	assert.Equal(t, "enterprise-node-agent", profileByName(t, profiles, "v1.1.0").Spec.Components[v1.SSHClusterType].NodeAgent.Tag)

	images, err := builder.BuildPackageImages("v1.1.0", v1.SSHClusterType, "npu-ascend910b")
	require.NoError(t, err)
	assert.Equal(t, "v1.1.0-npu-ascend910b", images[0].Tag)
}

func TestInjectCatalogRejectsInvalidLifecycleAndEligibility(t *testing.T) {
	t.Run("nil catalog", func(t *testing.T) {
		resetDefaultCatalogForTest(t)
		require.ErrorContains(t, InjectCatalog(nil), "catalog is required")
	})

	t.Run("different eligible profile set", func(t *testing.T) {
		resetDefaultCatalogForTest(t)

		spec := BuiltinCatalog().Spec()
		spec.ClusterProfiles = []*v1.ClusterProfile{profileByName(t, spec.ClusterProfiles, "v1.2.0")}
		catalog, err := NewCatalog(spec)
		require.NoError(t, err)
		require.ErrorContains(t, InjectCatalog(catalog), "eligible cluster profile versions")
	})

	t.Run("repeated injection", func(t *testing.T) {
		resetDefaultCatalogForTest(t)
		catalog := BuiltinCatalog()
		require.NoError(t, InjectCatalog(catalog))
		require.ErrorContains(t, InjectCatalog(catalog), "already injected")
	})

	t.Run("late injection", func(t *testing.T) {
		resetDefaultCatalogForTest(t)
		_ = NewBuilder()
		require.ErrorContains(t, InjectCatalog(BuiltinCatalog()), "already consumed")
	})
}

func resetDefaultCatalogForTest(t *testing.T) {
	t.Helper()

	defaultCatalogState.mu.Lock()
	previousCatalog := defaultCatalogState.catalog
	previousConsumed := defaultCatalogState.consumed
	previousInjected := defaultCatalogState.injected
	defaultCatalogState.catalog = builtinCatalog()
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
