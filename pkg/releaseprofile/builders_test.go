package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestCommunityBuildersBuildV12ReleaseInfoAndExactCompleteClusterProfiles(t *testing.T) {
	var releaseBuilder ReleaseInfoBuilder = NewCommunityReleaseInfoBuilder()
	var profileBuilder CurrentClusterProfileBuilder = NewCommunityClusterProfileBuilder()
	baselineProvider, ok := releaseBuilder.(CurrentReleaseInfoBaselineProvider)
	require.True(t, ok)
	assert.Equal(t, CurrentCommunityReleaseInfoBaseline, baselineProvider.CurrentReleaseInfoBaseline())

	info, err := releaseBuilder.BuildReleaseInfo(CurrentCommunityReleaseInfoBaseline)
	require.NoError(t, err)
	require.NotNil(t, info.Metadata)
	require.NotNil(t, info.Spec)
	assert.Equal(t, "v1", info.APIVersion)
	assert.Equal(t, v1.ReleaseInfoKind, info.Kind)
	assert.Equal(t, CurrentCommunityReleaseInfoBaseline, info.Metadata.Name)
	assert.Equal(t, "v1.2.0", info.Spec.DefaultClusterVersion)
	assert.Equal(t, []string{"v1.1", "v1.2"}, info.Spec.CompatibleClusterBaselines)

	profiles, err := profileBuilder.BuildClusterProfiles(CurrentCommunityReleaseInfoBaseline)
	require.NoError(t, err)
	require.Len(t, profiles, 3)

	var currentProfile *v1.ClusterProfile
	for _, profile := range profiles {
		if profile.GetName() == CurrentCommunityReleaseInfoBaseline {
			currentProfile = profile
			break
		}
	}
	require.NotNil(t, currentProfile)
	require.NotNil(t, currentProfile.Metadata)
	require.NotNil(t, currentProfile.Spec)
	assert.Equal(t, "v1", currentProfile.APIVersion)
	assert.Equal(t, v1.ClusterProfileKind, currentProfile.Kind)
	assert.Equal(t, CurrentCommunityReleaseInfoBaseline, currentProfile.Metadata.Name)

	sshProfile, found := currentProfile.Spec.ComponentsFor(v1.SSHClusterType)
	require.True(t, found)
	assert.Equal(t, v1.ClusterProfileComponents{
		RayRuntime:   v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.1.1"},
		NodeAgent:    v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.1.0-rc.1"},
		NodeExporter: v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
		VMAgent:      v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
	}, sshProfile)

	kubernetesProfile, found := currentProfile.Spec.ComponentsFor(v1.KubernetesClusterType)
	require.True(t, found)
	assert.Equal(t, v1.ClusterProfileComponents{
		KubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: "v1.1.1"},
		Router:            v1.ImageRef{Image: "neutree/router", Tag: "v1.1.1"},
		NodeAgent:         v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.1.0-rc.1"},
		NodeExporter:      v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
		VMAgent:           v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
		KubeStateMetrics:  v1.ImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
	}, kubernetesProfile)
}

func TestCommunityClusterProfileUsesExactVersionCatalog(t *testing.T) {
	profile, err := CommunityClusterProfile("v1.1.0")
	require.NoError(t, err)
	assert.Equal(t, "v1.1.0", profile.GetName())
	ssh, found := profile.Spec.ComponentsFor(v1.SSHClusterType)
	require.True(t, found)
	assert.Equal(t, "v1.1.0", ssh.RayRuntime.Tag)
	_, found = profile.Spec.ComponentsFor(v1.KubernetesClusterType)
	assert.True(t, found)

	_, err = CommunityClusterProfile("v1.2.3")
	require.ErrorContains(t, err, "unsupported")
}

func TestCommunityBuildersPreservePrereleaseControlPlaneIdentity(t *testing.T) {
	const baseline = "v1.2.0-alpha.1"

	info, err := NewCommunityReleaseInfoBuilder().BuildReleaseInfo(baseline)
	require.NoError(t, err)
	assert.Equal(t, baseline, info.GetName())

	profiles, err := NewCommunityClusterProfileBuilder().BuildClusterProfiles(baseline)
	require.NoError(t, err)
	assert.Len(t, profiles, 3)
}
