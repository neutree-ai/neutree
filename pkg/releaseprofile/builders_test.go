package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestCommunityBuildersBuildV12ReleaseInfoAndClusterProfile(t *testing.T) {
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

	profile, err := profileBuilder.BuildClusterProfile(CurrentCommunityReleaseInfoBaseline)
	require.NoError(t, err)
	require.NotNil(t, profile.Metadata)
	require.NotNil(t, profile.Spec)
	assert.Equal(t, "v1", profile.APIVersion)
	assert.Equal(t, v1.ClusterProfileKind, profile.Kind)
	assert.Equal(t, CurrentCommunityReleaseInfoBaseline, profile.Metadata.Name)
	assert.Equal(t, v1.ClusterProfileComponents{
		RayRuntime:       v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.1.1"},
		Router:           v1.ImageRef{Image: "neutree/router", Tag: "v1.1.1"},
		NodeAgent:        v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.1.0-rc.1"},
		NodeExporter:     v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
		VMAgent:          v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
		KubeStateMetrics: v1.ImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
	}, profile.Spec.Components)
}

func TestCommunityHistoricalClusterProfileSupportsSeededVersions(t *testing.T) {
	profile, err := CommunityHistoricalClusterProfile("v1.1.0")
	require.NoError(t, err)
	assert.Equal(t, "v1.1.0", profile.GetName())
	assert.Equal(t, "v1.1.0", profile.Spec.Components.RayRuntime.Tag)

	_, err = CommunityHistoricalClusterProfile("v1.3.0")
	require.ErrorContains(t, err, "not supported")
}
