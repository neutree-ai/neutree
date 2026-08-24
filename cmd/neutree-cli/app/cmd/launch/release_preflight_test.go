package launch

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
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
		[]*v1.ClusterProfile{preflightProfile("v1.1.1")},
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
		[]*v1.ClusterProfile{preflightProfile("v1.1.1")},
		preflightTargetReleaseInfo(),
		&output,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "1 incompatible Clusters")
	assert.Contains(t, output.String(), "v1.2.0/kubernetes has no exact ClusterProfile")
}

func TestRunReleasePreflightRejectsIncompleteEmbeddedCatalog(t *testing.T) {
	var output bytes.Buffer
	profile := preflightProfile("v1.1.1")
	profile.Spec.Components[v1.KubernetesClusterType] = v1.ClusterProfileComponents{}
	err := runReleasePreflight(
		&fakeClusterLister{},
		[]*v1.ClusterProfile{profile},
		preflightTargetReleaseInfo(),
		&output,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "incomplete")
}

func TestRunReleasePreflightRejectsDuplicateEmbeddedProfiles(t *testing.T) {
	var output bytes.Buffer
	err := runReleasePreflight(
		&fakeClusterLister{},
		[]*v1.ClusterProfile{preflightProfile("v1.1.1"), preflightProfile("v1.1.1")},
		preflightTargetReleaseInfo(),
		&output,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "duplicated")
}

func TestRunReleasePreflightRejectsInvalidEmbeddedProfileEnvelope(t *testing.T) {
	var output bytes.Buffer
	profile := preflightProfile("v1.1.1")
	profile.Kind = "WrongKind"

	err := runReleasePreflight(
		&fakeClusterLister{},
		[]*v1.ClusterProfile{profile},
		preflightTargetReleaseInfo(),
		&output,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "kind must be ClusterProfile")
}

func TestRunReleasePreflightRejectsDefaultOutsideCompatibleBaselines(t *testing.T) {
	var output bytes.Buffer
	target := preflightTargetReleaseInfo()
	target.Spec.CompatibleClusterBaselines = []string{"v1.1"}

	err := runReleasePreflight(
		&fakeClusterLister{},
		[]*v1.ClusterProfile{preflightProfile("v1.1.1")},
		target,
		&output,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "has incompatible baseline")
}

func TestBuildReleasePreflightTargetUsesCLIReleaseInfo(t *testing.T) {
	builder := builtinPreflightBuilder(t)
	target, err := buildReleasePreflightTargetWithBuilder("v1.2.0-alpha.1", builder)

	require.NoError(t, err)
	assert.Equal(t, "v1.2.0-alpha.1", target.GetName())
	assert.Equal(t, []string{"v1.1", "v1.2"}, target.Spec.CompatibleClusterBaselines)
	assert.Equal(t, "v1.2.0", target.Spec.DefaultClusterVersion)

	target, err = buildReleasePreflightTargetWithBuilder("b64e294", builder)
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", target.GetName())

	_, err = buildReleasePreflightTargetWithBuilder("dev", builder)
	require.Error(t, err)

	_, err = buildReleasePreflightTargetWithBuilder("DEADBEEF", builder)
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

func TestNewNeutreeCorePreflightCmdDefersBuilderCreation(t *testing.T) {
	builderCreated := false
	command := newNeutreeCorePreflightCmd(func() releaseprofile.Builder {
		builderCreated = true

		return &preflightReleaseInfoBuilder{}
	})

	assert.False(t, builderCreated)
	assert.NotNil(t, command.RunE)
}

func builtinPreflightBuilder(t *testing.T) releaseprofile.Builder {
	t.Helper()

	builder, err := releaseprofile.NewBuilderForCatalog(releaseprofile.BuiltinCatalog())
	require.NoError(t, err)

	return builder
}

func preflightTargetReleaseInfo() *v1.ReleaseInfo {
	return &v1.ReleaseInfo{
		APIVersion: "v1",
		Kind:       v1.ReleaseInfoKind,
		Metadata:   &v1.Metadata{Name: "v1.2.0"},
		Spec: &v1.ReleaseInfoSpec{
			DefaultClusterVersion:      "v1.2.0",
			CompatibleClusterBaselines: []string{"v1.1", "v1.2"},
		},
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

type preflightReleaseInfoBuilder struct {
	baseline    string
	compatibles []string
}

func (builder *preflightReleaseInfoBuilder) BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error) {
	return &v1.ReleaseInfo{
		Metadata: &v1.Metadata{Name: baseline},
		Spec: &v1.ReleaseInfoSpec{
			DefaultClusterVersion:      "v1.3.0",
			CompatibleClusterBaselines: append([]string(nil), builder.compatibles...),
		},
	}, nil
}

func (builder *preflightReleaseInfoBuilder) CurrentReleaseInfoBaseline() string {
	return builder.baseline
}

func (builder *preflightReleaseInfoBuilder) BuildClusterProfiles(string) ([]*v1.ClusterProfile, error) {
	return nil, nil
}

func (builder *preflightReleaseInfoBuilder) BuildPackageImages(string, string, string) ([]v1.ImageRef, error) {
	return nil, nil
}

func (builder *preflightReleaseInfoBuilder) PackageAccelerators(string) []string {
	return nil
}

func preflightProfile(version string) *v1.ClusterProfile {
	return &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: version},
		Spec: &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{
			v1.SSHClusterType: {
				RayRuntime:   v1.ImageRef{Image: "neutree/serve", Tag: version},
				NodeAgent:    v1.ImageRef{Image: "neutree/node-agent", Tag: version},
				NodeExporter: v1.ImageRef{Image: "prom/node-exporter", Tag: version},
				VMAgent:      v1.ImageRef{Image: "vmagent", Tag: version},
			},
			v1.KubernetesClusterType: {
				KubernetesRuntime: v1.ImageRef{Image: "neutree/runtime", Tag: version},
				Router:            v1.ImageRef{Image: "neutree/router", Tag: version},
				NodeAgent:         v1.ImageRef{Image: "neutree/node-agent", Tag: version},
				NodeExporter:      v1.ImageRef{Image: "prom/node-exporter", Tag: version},
				VMAgent:           v1.ImageRef{Image: "vmagent", Tag: version},
				KubeStateMetrics:  v1.ImageRef{Image: "kube-state-metrics", Tag: version},
			},
		}},
	}
}
