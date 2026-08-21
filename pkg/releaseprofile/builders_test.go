package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestCommunityBuildersBuildV12ReleaseInfoAndClusterProfiles(t *testing.T) {
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
	assert.Equal(t, []string{"v1.1", "v1.2"}, info.Spec.CompatibleClusterBaselines)

	sshProfile, err := profileBuilder.BuildClusterProfile(CurrentCommunityReleaseInfoBaseline, v1.SSHClusterType)
	require.NoError(t, err)
	require.NotNil(t, sshProfile.Metadata)
	require.NotNil(t, sshProfile.Spec)
	assert.Equal(t, "v1", sshProfile.APIVersion)
	assert.Equal(t, v1.ClusterProfileKind, sshProfile.Kind)
	assert.Equal(t, CurrentCommunityReleaseInfoBaseline, sshProfile.Metadata.Name)
	assert.Equal(t, v1.SSHClusterType, sshProfile.GetClusterType())
	assert.Equal(t, v1.ClusterProfileComponents{
		RayRuntime:   v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.1.1"},
		NodeAgent:    v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.1.0-rc.1"},
		NodeExporter: v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
		VMAgent:      v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
	}, sshProfile.Spec.Components)

	kubernetesProfile, err := profileBuilder.BuildClusterProfile(CurrentCommunityReleaseInfoBaseline, v1.KubernetesClusterType)
	require.NoError(t, err)
	assert.Equal(t, v1.KubernetesClusterType, kubernetesProfile.GetClusterType())
	assert.Equal(t, v1.ClusterProfileComponents{
		KubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: "v1.1.1"},
		Router:            v1.ImageRef{Image: "neutree/router", Tag: "v1.1.1"},
		NodeAgent:         v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.1.0-rc.1"},
		NodeExporter:      v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
		VMAgent:           v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
		KubeStateMetrics:  v1.ImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
	}, kubernetesProfile.Spec.Components)
}

func TestCommunityHistoricalClusterProfileSupportsSeededVersions(t *testing.T) {
	profile, err := CommunityHistoricalClusterProfile("v1.1.0", v1.SSHClusterType)
	require.NoError(t, err)
	assert.Equal(t, "v1.1.0", profile.GetName())
	assert.Equal(t, v1.SSHClusterType, profile.GetClusterType())
	assert.Equal(t, "v1.1.0", profile.Spec.Components.RayRuntime.Tag)

	_, err = CommunityHistoricalClusterProfile("v1.3.0", v1.SSHClusterType)
	require.ErrorContains(t, err, "unsupported")
}
