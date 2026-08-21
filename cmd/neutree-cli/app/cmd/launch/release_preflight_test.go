package launch

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/client"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

func TestRunReleasePreflightReportsEveryIncompatibleCluster(t *testing.T) {
	clusters := []v1.Cluster{
		{
			Metadata: &v1.Metadata{Name: "accepted-v11", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.2.0"},
			Status:   &v1.ClusterStatus{Version: "v1.1.1"},
		},
		{
			Metadata: &v1.Metadata{Name: "incompatible-status", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.2.0"},
			Status:   &v1.ClusterStatus{Version: "v1.3.0-alpha.1"},
		},
		{
			Metadata: &v1.Metadata{Name: "incompatible-spec", Workspace: "other"},
			Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.3.0"},
		},
	}
	rawClusters := make([]json.RawMessage, 0, len(clusters))
	for _, cluster := range clusters {
		payload, err := json.Marshal(cluster)
		require.NoError(t, err)
		rawClusters = append(rawClusters, payload)
	}
	var output bytes.Buffer

	err := runReleasePreflight(
		&fakeClusterLister{clusters: rawClusters},
		&fakeClusterProfileVersionLister{profiles: []client.ClusterProfileVersion{{Version: "v1.1.1", ClusterType: v1.KubernetesClusterType}}},
		preflightTargetReleaseInfo(),
		&output,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "2 incompatible Clusters")
	assert.Contains(t, output.String(), "default/incompatible-status")
	assert.Contains(t, output.String(), "v1.3.0-alpha.1")
	assert.Contains(t, output.String(), "other/incompatible-spec")
	assert.Contains(t, output.String(), "v1.3.0")
	assert.NotContains(t, output.String(), "accepted-v11")
}

func TestRunReleasePreflightRejectsMissingExactProfileForProfileAwareCluster(t *testing.T) {
	cluster := v1.Cluster{
		Metadata: &v1.Metadata{Name: "needs-profile", Workspace: "default"},
		Spec:     &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.2.0"},
		Status:   &v1.ClusterStatus{Version: "v1.2.0"},
	}
	rawCluster, err := json.Marshal(cluster)
	require.NoError(t, err)

	var output bytes.Buffer
	err = runReleasePreflight(
		&fakeClusterLister{clusters: []json.RawMessage{rawCluster}},
		&fakeClusterProfileVersionLister{profiles: []client.ClusterProfileVersion{{Version: "v1.2.0", ClusterType: v1.SSHClusterType}}},
		preflightTargetReleaseInfo(),
		&output,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "1 incompatible Clusters")
	assert.Contains(t, output.String(), "v1.2.0/kubernetes has no exact ClusterProfile")
}

func TestBuildReleasePreflightTargetUsesCLIReleaseInfo(t *testing.T) {
	target, err := buildReleasePreflightTarget("v1.2.0-alpha.1")

	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", target.GetName())
	assert.Equal(t, []string{"v1.1", "v1.2"}, target.Spec.CompatibleClusterBaselines)

	target, err = buildReleasePreflightTarget("b64e294")
	require.NoError(t, err)
	assert.Equal(t, releaseprofile.CurrentCommunityReleaseInfoBaseline, target.GetName())

	_, err = buildReleasePreflightTarget("dev")
	require.Error(t, err)

	_, err = buildReleasePreflightTarget("DEADBEEF")
	require.Error(t, err)
}

func TestBuildReleasePreflightTargetWithBuilderUsesInjectedWorkflowBaseline(t *testing.T) {
	builder := &preflightReleaseInfoBuilder{
		baseline:    "v1.3.0",
		compatibles: []string{"v1.2", "v1.3"},
	}

	target, err := buildReleasePreflightTargetWithBuilder("b64e294", builder)

	require.NoError(t, err)
	assert.Equal(t, "v1.3.0", target.GetName())
	assert.Equal(t, []string{"v1.2", "v1.3"}, target.Spec.CompatibleClusterBaselines)
}

func TestNeutreeCorePreflightDoesNotInheritInstallOnlyFlags(t *testing.T) {
	install := NewNeutreeCoreInstallCmd(nil, &commonOptions{})
	preflight, _, err := install.Find([]string{"preflight"})

	require.NoError(t, err)
	require.NotNil(t, preflight)
	for _, flagName := range []string{"jwt-secret", "model-scope-endpoint", "admin-password"} {
		assert.Nil(t, preflight.Flags().Lookup(flagName))
		assert.Nil(t, preflight.InheritedFlags().Lookup(flagName))
	}
	assert.NotNil(t, preflight.RunE)
}

func preflightTargetReleaseInfo() *v1.ReleaseInfo {
	return &v1.ReleaseInfo{
		Metadata: &v1.Metadata{Name: "v1.2.0"},
		Spec:     &v1.ReleaseInfoSpec{CompatibleClusterBaselines: []string{"v1.1", "v1.2"}},
	}
}

type fakeClusterLister struct {
	clusters []json.RawMessage
	err      error
}

type fakeClusterProfileVersionLister struct {
	profiles []client.ClusterProfileVersion
	err      error
}

func (lister *fakeClusterProfileVersionLister) ListClusterProfileVersions() ([]client.ClusterProfileVersion, error) {
	return lister.profiles, lister.err
}

func (lister *fakeClusterLister) List(kind, workspace string) ([]json.RawMessage, error) {
	if kind != "Cluster" || workspace != "" {
		return nil, assert.AnError
	}

	return lister.clusters, lister.err
}

type preflightReleaseInfoBuilder struct {
	baseline    string
	compatibles []string
}

func (builder *preflightReleaseInfoBuilder) BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error) {
	return &v1.ReleaseInfo{
		Metadata: &v1.Metadata{Name: baseline},
		Spec:     &v1.ReleaseInfoSpec{CompatibleClusterBaselines: append([]string(nil), builder.compatibles...)},
	}, nil
}

func (builder *preflightReleaseInfoBuilder) CurrentReleaseInfoBaseline() string {
	return builder.baseline
}
