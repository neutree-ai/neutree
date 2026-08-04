package cluster

import (
	"bytes"
	"errors"
	"testing"

	"github.com/neutree-ai/neutree/pkg/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCheckPrintsAllowedPreflight(t *testing.T) {
	apiClient := &fakePreflightClient{result: &client.ClusterUpgradePreflight{
		Allowed:       true,
		SourceVersion: "v1.1.0",
		TargetVersion: "v1.2.0",
		UpgradeTo:     []string{"v1.1.1", "v1.2.0"},
		ReleaseInfo: client.ClusterReleaseInfoReference{
			Baseline: "v1.2.0",
			Revision: "revision-2",
		},
	}}
	var output bytes.Buffer

	err := runCheck(apiClient, checkOptions{name: "cluster-a", workspace: "default", targetVersion: "v1.2.0"}, &output)

	require.NoError(t, err)
	assert.Equal(t, "default", apiClient.workspace)
	assert.Equal(t, "cluster-a", apiClient.name)
	assert.Equal(t, "v1.2.0", apiClient.targetVersion)
	assert.JSONEq(t, `{"allowed":true,"source_version":"v1.1.0","target_version":"v1.2.0","upgrade_to":["v1.1.1","v1.2.0"],"release_info":{"baseline":"v1.2.0","revision":"revision-2"}}`, output.String())
}

func TestRunCheckReturnsPreflightFailure(t *testing.T) {
	apiClient := &fakePreflightClient{err: errors.New("not allowed")}

	err := runCheck(apiClient, checkOptions{name: "cluster-a", workspace: "default", targetVersion: "v1.1.0"}, &bytes.Buffer{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "upgrade preflight")
}

type fakePreflightClient struct {
	result        *client.ClusterUpgradePreflight
	err           error
	workspace     string
	name          string
	targetVersion string
}

func (client *fakePreflightClient) UpgradePreflight(workspace, name, targetVersion string) (*client.ClusterUpgradePreflight, error) {
	client.workspace = workspace
	client.name = name
	client.targetVersion = targetVersion

	return client.result, client.err
}
