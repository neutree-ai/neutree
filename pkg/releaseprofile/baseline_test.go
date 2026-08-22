package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestNormalizeClusterMinorPreservesPrereleaseLine(t *testing.T) {
	minor, err := NormalizeClusterMinor("v1.2.0-alpha.1")
	require.NoError(t, err)
	assert.Equal(t, "v1.2", minor)

	_, err = NormalizeClusterMinor("1.2.0")
	require.ErrorContains(t, err, "v-prefixed")
}

func TestResolveCurrentControlPlaneBaselineUsesHighestPersistedReleaseForDevelopmentBuild(t *testing.T) {
	baseline, err := ResolveCurrentControlPlaneBaseline("dev-dirty", []v1.ReleaseInfo{
		{Metadata: &v1.Metadata{Name: "invalid"}},
		{Metadata: &v1.Metadata{Name: "v1.1.0"}},
		{Metadata: &v1.Metadata{Name: "v1.2.0-alpha.1"}},
		{Metadata: &v1.Metadata{Name: "v1.2.0"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", baseline)
}

func TestNormalizeControlPlaneReleaseKeepsPrereleaseIdentity(t *testing.T) {
	identity, err := NormalizeControlPlaneRelease("v1.2.0-alpha.1")
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0-alpha.1", identity)
}
