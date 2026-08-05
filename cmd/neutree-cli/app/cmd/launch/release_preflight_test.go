package launch

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestRunReleasePreflightReportsEveryIncompatibleCluster(t *testing.T) {
	clusters := []v1.Cluster{
		{
			Metadata: &v1.Metadata{Name: "accepted-v11", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.2.0"},
			Status:   &v1.ClusterStatus{Version: "v1.1.1"},
		},
		{
			Metadata: &v1.Metadata{Name: "incompatible-status", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.2.0"},
			Status:   &v1.ClusterStatus{Version: "v1.3.0-alpha.1"},
		},
		{
			Metadata: &v1.Metadata{Name: "incompatible-spec", Workspace: "other"},
			Spec:     &v1.ClusterSpec{Version: "v1.3.0"},
		},
	}
	rawClusters := make([]json.RawMessage, 0, len(clusters))
	for _, cluster := range clusters {
		payload, err := json.Marshal(cluster)
		require.NoError(t, err)
		rawClusters = append(rawClusters, payload)
	}
	var output bytes.Buffer

	err := runReleasePreflight(&fakeClusterLister{clusters: rawClusters}, preflightTargetReleaseInfo(), &output)

	require.Error(t, err)
	assert.ErrorContains(t, err, "2 incompatible Clusters")
	assert.Contains(t, output.String(), "default/incompatible-status")
	assert.Contains(t, output.String(), "v1.3.0-alpha.1")
	assert.Contains(t, output.String(), "other/incompatible-spec")
	assert.Contains(t, output.String(), "v1.3.0")
	assert.NotContains(t, output.String(), "accepted-v11")
}

func TestBuildReleasePreflightTargetUsesCLIReleaseInfo(t *testing.T) {
	target, err := buildReleasePreflightTarget("v1.2.0-alpha.1")

	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", target.GetName())
	assert.Equal(t, []string{"v1.1", "v1.2"}, target.Spec.CompatibleClusterBaselines)

	_, err = buildReleasePreflightTarget("dev")
	require.Error(t, err)
}

func TestNeutreeCorePreflightDoesNotInheritInstallOnlyFlags(t *testing.T) {
	install := NewNeutreeCoreInstallCmd(nil, &commonOptions{})
	preflight, _, err := install.Find([]string{"preflight"})

	require.NoError(t, err)
	require.NotNil(t, preflight)
	assert.Nil(t, preflight.Flags().Lookup("jwt-secret"))
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

func (lister *fakeClusterLister) List(kind, workspace string) ([]json.RawMessage, error) {
	if kind != "Cluster" || workspace != "" {
		return nil, assert.AnError
	}

	return lister.clusters, lister.err
}
