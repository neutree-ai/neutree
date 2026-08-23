package releaseprofile

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestProviderResolvesCurrentReleaseAndExactProfile(t *testing.T) {
	reader := &providerReader{
		infos: []v1.ReleaseInfo{
			*releaseInfoForTest("v1.1.0", "v1.1.0", []string{"v1.1"}),
			*releaseInfoForTest("v1.2.0", "v1.2.0", []string{"v1.1", "v1.2"}),
		},
		profiles: []v1.ClusterProfile{*completeProfileForTest("v1.2.0")},
	}

	info, err := NewReleaseInfoProvider(reader, "v1.2.0").Current()
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", info.GetName())

	provider := NewClusterProfileProvider(reader)
	components, err := provider.ComponentsFor("v1.2.0", v1.KubernetesClusterType)
	require.NoError(t, err)
	assert.Equal(t, "neutree/neutree-runtime", components.KubernetesRuntime.Image)
}

func TestClusterProfileProviderDoesNotFallbackFromExactVersion(t *testing.T) {
	provider := NewClusterProfileProvider(&providerReader{profiles: []v1.ClusterProfile{*completeProfileForTest("v1.2.0")}})

	_, err := provider.ProfileFor("v1.2.0-alpha.1")
	require.ErrorContains(t, err, "cluster profile v1.2.0-alpha.1 not found")
}

func TestProviderReportsReaderErrors(t *testing.T) {
	infoProvider := NewReleaseInfoProvider(&providerReader{infoErr: errors.New("database unavailable")}, "v1.2.0")
	_, err := infoProvider.Current()
	require.ErrorContains(t, err, "list release infos")

	profileProvider := NewClusterProfileProvider(&providerReader{profileErr: errors.New("database unavailable")})
	_, err = profileProvider.ProfileFor("v1.2.0")
	require.ErrorContains(t, err, "list cluster profiles")
}

func TestClusterProfileReaderFuncAdaptsQueries(t *testing.T) {
	provider := NewClusterProfileProvider(ClusterProfileReaderFunc(func() ([]v1.ClusterProfile, error) {
		return []v1.ClusterProfile{*completeProfileForTest("v1.2.0")}, nil
	}))

	profile, err := provider.ProfileFor("v1.2.0")
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", profile.GetName())
}

func TestResolveCurrentControlPlaneBaselineUsesPersistedDataForDevelopmentBuilds(t *testing.T) {
	baseline, err := ResolveCurrentControlPlaneBaseline("dev-dirty", []v1.ReleaseInfo{
		{Metadata: &v1.Metadata{Name: "invalid"}},
		{Metadata: &v1.Metadata{Name: "v1.1.0"}},
		{Metadata: &v1.Metadata{Name: "v1.2.0-alpha.1"}},
		{Metadata: &v1.Metadata{Name: "v1.2.0"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", baseline)

	identity, err := ResolveCurrentControlPlaneBaseline("v1.2.0-rc.1", nil)
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0-rc.1", identity)
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

func (reader *providerReader) ListClusterProfile() ([]v1.ClusterProfile, error) {
	return reader.profiles, reader.profileErr
}
