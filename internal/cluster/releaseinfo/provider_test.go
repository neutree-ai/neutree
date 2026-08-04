package releaseinfo

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestProviderResolvesCurrentBaselineAndAcceleratorOverrides(t *testing.T) {
	store := &releaseInfoReader{infos: []v1.ReleaseInfo{
		*mustSeed(t, "v1.1.0"),
		*mustSeed(t, "v1.2.0"),
	}}
	provider := NewProvider(store, "v1.2.0-nightly.20260804")

	info, err := provider.Current()
	require.NoError(t, err)
	require.Equal(t, "v1.2.0", info.GetName())

	components, err := provider.ComponentsFor("v1.2.0", "amd_gpu")
	require.NoError(t, err)
	assert.Equal(t, "neutree/neutree-serve:v1.1.1-rocm", components["ray_runtime"])
	assert.Equal(t, "neutree/router:v1.1.1", components["router"])
	assert.Equal(t, "neutree/neutree-node-agent:v1.1.0-rc.1", components["node_agent"])
}

func TestProviderReturnsErrorWhenCurrentBaselineIsMissing(t *testing.T) {
	provider := NewProvider(&releaseInfoReader{infos: []v1.ReleaseInfo{*mustSeed(t, "v1.1.0")}}, "v1.2.0")

	_, err := provider.Current()
	require.ErrorContains(t, err, "release info v1.2.0 not found")
}

func TestProviderReturnsStorageError(t *testing.T) {
	provider := NewProvider(&releaseInfoReader{err: errors.New("database unavailable")}, "v1.2.0")

	_, err := provider.Current()
	require.ErrorContains(t, err, "list release infos")
}

type releaseInfoReader struct {
	infos []v1.ReleaseInfo
	err   error
}

func (reader *releaseInfoReader) ListReleaseInfo() ([]v1.ReleaseInfo, error) {
	return reader.infos, reader.err
}
