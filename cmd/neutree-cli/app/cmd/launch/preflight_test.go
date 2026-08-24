package launch

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

func TestRunNeutreeCorePreflightReportsEveryIncompatibleCluster(t *testing.T) {
	clusters := []v1.Cluster{
		{
			Metadata: &v1.Metadata{Name: "accepted-status", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.2.0"},
			Status:   &v1.ClusterStatus{Version: "v1.1.1"},
		},
		{
			Metadata: &v1.Metadata{Name: "accepted-prerelease", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.2.0-alpha.1"},
		},
		{
			Metadata: &v1.Metadata{Name: "above-default", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.2.1"},
		},
		{
			Metadata: &v1.Metadata{Name: "incompatible-minor", Workspace: "other"},
			Spec:     &v1.ClusterSpec{Version: "v1.3.0"},
		},
	}
	rawClusters := marshalPreflightClusters(t, clusters)
	var output bytes.Buffer

	err := runNeutreeCorePreflight(rawClusters, preflightReleaseInfo(), &output)

	require.Error(t, err)
	assert.ErrorContains(t, err, "2 incompatible Clusters")
	assert.NotContains(t, output.String(), "accepted-status")
	assert.NotContains(t, output.String(), "accepted-prerelease")
	assert.Contains(t, output.String(), "default/above-default")
	assert.Contains(t, output.String(), "other/incompatible-minor")
}

func TestRunNeutreeCorePreflightRejectsInvalidEffectiveVersion(t *testing.T) {
	clusters := []v1.Cluster{{
		Metadata: &v1.Metadata{Name: "invalid-status"},
		Spec:     &v1.ClusterSpec{Version: "v1.1.1"},
		Status:   &v1.ClusterStatus{Version: "invalid"},
	}}
	var output bytes.Buffer

	err := runNeutreeCorePreflight(marshalPreflightClusters(t, clusters), preflightReleaseInfo(), &output)

	require.Error(t, err)
	assert.ErrorContains(t, err, "1 incompatible Clusters")
	assert.Contains(t, output.String(), "default/invalid-status")
	assert.Contains(t, output.String(), "invalid cluster version")
}

func TestBuildNeutreeCorePreflightTarget(t *testing.T) {
	builder := preflightBuilder{baseline: "v1.2.0"}

	target, err := buildNeutreeCorePreflightTarget("v1.2.0", builder)
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", target.GetName())

	target, err = buildNeutreeCorePreflightTarget("v1.2.0-rc.1", builder)
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0-rc.1", target.GetName())

	target, err = buildNeutreeCorePreflightTarget("b64e294", builder)
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", target.GetName())

	_, err = buildNeutreeCorePreflightTarget("dev", builder)
	require.ErrorContains(t, err, "cannot derive")

	_, err = buildNeutreeCorePreflightTarget("v1.2.0-dirty", builder)
	require.ErrorContains(t, err, "cannot derive")
}

func TestNormalizeControlPlaneRelease(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		version string
		wantErr string
	}{
		{name: "stable release", version: "v1.2.0"},
		{name: "prerelease", version: "v1.2.0-rc.1"},
		{name: "missing patch", version: "v1.2", wantErr: "invalid control-plane release"},
		{name: "missing prefix", version: "1.2.0", wantErr: "invalid control-plane release"},
		{name: "whitespace", version: "v1.2.0 ", wantErr: "invalid control-plane release"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			actual, err := normalizeControlPlaneRelease(testCase.version)
			if testCase.wantErr != "" {
				require.ErrorContains(t, err, testCase.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.version, actual)
		})
	}
}

func TestNeutreeCorePreflightUsesGlobalGenericClient(t *testing.T) {
	previousServerURL, previousAPIKey, previousInsecure := global.ServerURL, global.APIKey, global.Insecure
	previousVersion := getCLIAppVersion
	t.Cleanup(func() {
		global.ServerURL, global.APIKey, global.Insecure = previousServerURL, previousAPIKey, previousInsecure
		getCLIAppVersion = previousVersion
	})

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "/api/v1/clusters", request.URL.Path)
		assert.Equal(t, "test-api-key", request.Header.Get("Authorization"))
		_, _ = writer.Write([]byte(`[{"metadata":{"name":"cluster-a","workspace":"default"},"spec":{"version":"v1.1.1"}}]`))
	}))
	defer server.Close()

	global.ServerURL = server.URL
	global.APIKey = "test-api-key"
	global.Insecure = false
	getCLIAppVersion = func() string { return "v1.2.0" }

	command := NewNeutreeCorePreflightCmdWithBuilder(preflightBuilder{baseline: "v1.2.0"})
	command.SetOut(&bytes.Buffer{})

	require.NoError(t, command.Execute())
}

func TestNeutreeCorePreflightRequiresGlobalCredentials(t *testing.T) {
	previousServerURL, previousAPIKey := global.ServerURL, global.APIKey
	previousVersion := getCLIAppVersion
	t.Cleanup(func() {
		global.ServerURL, global.APIKey = previousServerURL, previousAPIKey
		getCLIAppVersion = previousVersion
	})

	global.ServerURL = ""
	global.APIKey = ""
	getCLIAppVersion = func() string { return "v1.2.0" }

	command := NewNeutreeCorePreflightCmdWithBuilder(preflightBuilder{baseline: "v1.2.0"})
	require.ErrorContains(t, command.Execute(), "server URL is required")
}

func TestNeutreeCorePreflightDoesNotInheritInstallOnlyFlags(t *testing.T) {
	install := NewNeutreeCoreInstallCmd(nil, &commonOptions{})
	preflight, _, err := install.Find([]string{"preflight"})

	require.NoError(t, err)
	require.NotNil(t, preflight)
	for _, flagName := range []string{"jwt-secret", "model-scope-endpoint", "admin-password", "version"} {
		assert.Nil(t, preflight.Flags().Lookup(flagName))
		assert.Nil(t, preflight.InheritedFlags().Lookup(flagName))
	}
}

func marshalPreflightClusters(t *testing.T, clusters []v1.Cluster) []json.RawMessage {
	t.Helper()

	rawClusters := make([]json.RawMessage, 0, len(clusters))
	for _, cluster := range clusters {
		payload, err := json.Marshal(cluster)
		require.NoError(t, err)
		rawClusters = append(rawClusters, payload)
	}

	return rawClusters
}

func preflightReleaseInfo() *v1.ReleaseInfo {
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

type preflightBuilder struct {
	baseline string
}

func (builder preflightBuilder) CurrentReleaseInfoBaseline() string {
	return builder.baseline
}

func (builder preflightBuilder) BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error) {
	info := preflightReleaseInfo()
	info.Metadata.Name = baseline

	return info, nil
}

func (preflightBuilder) BuildClusterProfiles(string) ([]*v1.ClusterProfile, error) {
	return nil, nil
}

var _ releaseprofile.Builder = preflightBuilder{}
