package releaseprofile

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestProviderResolvesCurrentReleaseAndExactProfile(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)
	current, err := builder.BuildReleaseInfo("v1.2.0")
	require.NoError(t, err)
	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)

	reader := &providerReader{
		infos: []v1.ReleaseInfo{
			{Metadata: &v1.Metadata{Name: "v1.1.0"}},
			*current,
		},
		profiles: []v1.ClusterProfile{*profileByName(t, profiles, "v1.2.0")},
	}

	provider := NewProvider(reader, reader)
	info, err := provider.Current()
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", info.GetName())

	components, err := provider.ComponentsFor("v1.2.0", v1.KubernetesClusterType)
	require.NoError(t, err)
	assert.Equal(t, "neutree/neutree-runtime", components.KubernetesRuntime.Image)
}

func TestClusterProfileProviderDoesNotFallbackFromExactVersion(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)
	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)

	reader := &providerReader{profiles: []v1.ClusterProfile{*profileByName(t, profiles, "v1.2.0")}}
	provider := NewProvider(reader, reader)

	_, err = provider.ProfileFor("v1.2.0-alpha.1")
	require.ErrorContains(t, err, "cluster profile v1.2.0-alpha.1 not found")
}

func TestProviderReportsReaderErrors(t *testing.T) {
	provider := NewProvider(&providerReader{infoErr: errors.New("database unavailable")}, &providerReader{profileErr: errors.New("database unavailable")})
	_, err := provider.Current()
	require.ErrorContains(t, err, "list release infos")

	_, err = provider.ProfileFor("v1.2.0")
	require.ErrorContains(t, err, "list cluster profiles")
}

type providerReader struct {
	infos      []v1.ReleaseInfo
	profiles   []v1.ClusterProfile
	infoErr    error
	profileErr error
}

func (reader *providerReader) ListReleaseInfo() ([]v1.ReleaseInfo, error) {
	return reader.infos, reader.infoErr
}

func (*providerReader) CreateReleaseInfo(*v1.ReleaseInfo) error {
	return nil
}

func (*providerReader) UpdateReleaseInfo(string, *v1.ReleaseInfo) error {
	return nil
}

func (reader *providerReader) ListClusterProfile(storage.ListOption) ([]v1.ClusterProfile, error) {
	return reader.profiles, reader.profileErr
}

func (*providerReader) CreateClusterProfile(*v1.ClusterProfile) error {
	return nil
}

func (*providerReader) UpdateClusterProfile(string, *v1.ClusterProfile) error {
	return nil
}
