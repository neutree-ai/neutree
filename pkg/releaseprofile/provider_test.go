package releaseprofile

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestProviderResolvesCurrentReleaseInfoAndExactClusterProfile(t *testing.T) {
	reader := &providerReader{
		infos: []v1.ReleaseInfo{
			*releaseInfoNamed("v1.1.0"),
			*releaseInfoNamed("v1.2.0"),
			*releaseInfoNamed("v1.2.0-nightly.20260804"),
		},
		profiles: []v1.ClusterProfile{
			profileForProvider("v1.2.0", "stable", "stable-kubernetes"),
			profileForProvider("v1.2.0-rc.1", "release-candidate", "kubernetes-release-candidate"),
		},
	}

	info, err := NewReleaseInfoProvider(reader, "v1.2.0-nightly.20260804").Current()
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0-nightly.20260804", info.GetName())

	provider := NewClusterProfileProvider(reader)
	resolved, err := provider.ProfileFor("v1.2.0-rc.1")
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0-rc.1", resolved.GetName())

	components, err := provider.ComponentsFor("v1.2.0-rc.1", v1.KubernetesClusterType)
	require.NoError(t, err)
	assert.Equal(t, "kubernetes-release-candidate", components.RayRuntime.Tag)
}

func TestProviderDoesNotFallBackFromExactClusterVersion(t *testing.T) {
	provider := NewClusterProfileProvider(&providerReader{profiles: []v1.ClusterProfile{
		profileForProvider("v1.2.0", "stable", "stable-kubernetes"),
	}})

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

type providerReader struct {
	infos      []v1.ReleaseInfo
	profiles   []v1.ClusterProfile
	infoErr    error
	profileErr error
}

func (reader *providerReader) ListReleaseInfo() ([]v1.ReleaseInfo, error) {
	return reader.infos, reader.infoErr
}

func (reader *providerReader) ListClusterProfiles() ([]v1.ClusterProfile, error) {
	return reader.profiles, reader.profileErr
}

func releaseInfoNamed(name string) *v1.ReleaseInfo {
	return &v1.ReleaseInfo{Metadata: &v1.Metadata{Name: name}}
}

func profileForProvider(name, sshRuntimeTag, kubernetesRuntimeTag string) v1.ClusterProfile {
	return v1.ClusterProfile{
		Metadata: &v1.Metadata{Name: name},
		Spec: &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{
			v1.SSHClusterType:        {RayRuntime: v1.ImageRef{Tag: sshRuntimeTag}},
			v1.KubernetesClusterType: {RayRuntime: v1.ImageRef{Tag: kubernetesRuntimeTag}},
		}},
	}
}
