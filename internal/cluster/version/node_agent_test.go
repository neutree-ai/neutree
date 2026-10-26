package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectNodeAgent(t *testing.T) {
	const (
		legacyImage  = "neutree/neutree-node-agent:v1.1.0-rc.1"
		pinnedImage  = "neutree/neutree-node-agent:v1.2.0-rc.1"
		currentImage = "neutree/neutree-node-agent:v1.2.1-rc.1"
	)

	tests := []struct {
		name     string
		version  string
		contract NodeAgentContract
		image    string
	}{
		{
			name:     "no version",
			version:  "",
			contract: NodeAgentContractLegacy,
			image:    legacyImage,
		},
		{
			name:     "last legacy version",
			version:  "v1.1.1",
			contract: NodeAgentContractLegacy,
			image:    legacyImage,
		},
		{
			name:     "legacy prerelease",
			version:  "v1.1.1-rc.1",
			contract: NodeAgentContractLegacy,
			image:    legacyImage,
		},
		{
			name:     "last pinned version",
			version:  "v1.2.0",
			contract: NodeAgentContractProfile,
			image:    pinnedImage,
		},
		{
			name:     "first version on the current image",
			version:  "v1.2.1",
			contract: NodeAgentContractProfile,
			image:    currentImage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection, err := SelectNodeAgent(tt.version)
			require.NoError(t, err)
			assert.Equal(t, tt.contract, selection.Contract)
			assert.Equal(t, tt.image, selection.Image)
		})
	}
}

func TestSelectNodeAgentRejectsUnparsableVersion(t *testing.T) {
	_, err := SelectNodeAgent("not-a-version")
	require.Error(t, err)
}
