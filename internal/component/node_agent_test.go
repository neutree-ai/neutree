package component

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestSelectNodeAgentUsesLegacyImageForLegacyVersions(t *testing.T) {
	for _, version := range []string{"v1.1.0", "v1.1.1", "v1.1.1-rc.1"} {
		t.Run(version, func(t *testing.T) {
			selection, err := SelectNodeAgent(version, &v1.NodeAgentRuntimeProfile{Image: "registry.example.com/neutree/neutree-node-agent:v1.2.0"})
			require.NoError(t, err)
			assert.Equal(t, NodeAgentContractLegacy, selection.Contract)
			assert.Equal(t, defaultLegacyNodeAgentImage(), selection.Image)
		})
	}
}

func TestSelectNodeAgentUsesDefaultProfileImageWhenProfileMissing(t *testing.T) {
	selection, err := SelectNodeAgent("v1.1.2", nil)
	require.NoError(t, err)
	assert.Equal(t, NodeAgentContractProfile, selection.Contract)
	assert.Equal(t, defaultProfileNodeAgentImage(), selection.Image)
}

func TestSelectNodeAgentUsesProfileImageForNewerVersions(t *testing.T) {
	selection, err := SelectNodeAgent("v1.1.2", &v1.NodeAgentRuntimeProfile{Image: "registry.example.com/neutree/neutree-node-agent:v1.2.0"})
	require.NoError(t, err)
	assert.Equal(t, NodeAgentContractProfile, selection.Contract)
	assert.Equal(t, "registry.example.com/neutree/neutree-node-agent:v1.2.0", selection.Image)
}

func TestSelectNodeAgentRejectsEmptyProfileImage(t *testing.T) {
	_, err := SelectNodeAgent("v1.1.2", &v1.NodeAgentRuntimeProfile{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "node agent profile image is required")
}
