package releaseinfo

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestProviderResolvesCurrentBaseline(t *testing.T) {
	store := &releaseInfoReader{infos: []v1.ReleaseInfo{
		*releaseInfoNamed("v1.1.0"),
		*releaseInfoNamed("v1.2.0"),
		*releaseInfoNamed("v1.2.0-nightly.20260804"),
	}}
	provider := NewProvider(store, "v1.2.0-nightly.20260804")

	info, err := provider.Current()
	require.NoError(t, err)
	require.Equal(t, "v1.2.0-nightly.20260804", info.GetName())

}

func TestProviderReturnsErrorWhenCurrentBaselineIsMissing(t *testing.T) {
	provider := NewProvider(&releaseInfoReader{infos: []v1.ReleaseInfo{*releaseInfoNamed("v1.1.0")}}, "v1.2.0")

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
