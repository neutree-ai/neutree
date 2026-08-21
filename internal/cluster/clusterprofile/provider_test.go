package clusterprofile

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestProviderResolvesExactClusterProfileIdentity(t *testing.T) {
	provider := NewProvider(&reader{profiles: []v1.ClusterProfile{
		profile("v1.2.0", v1.SSHClusterType, "stable"),
		profile("v1.2.0-rc.1", v1.SSHClusterType, "release-candidate"),
		profile("v1.2.0-rc.1", v1.KubernetesClusterType, "kubernetes-release-candidate"),
	}})

	resolved, err := provider.ProfileFor("v1.2.0-rc.1", v1.SSHClusterType)
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0-rc.1", resolved.GetName())
	assert.Equal(t, v1.SSHClusterType, resolved.GetClusterType())

	components, err := provider.ComponentsFor("v1.2.0-rc.1", v1.KubernetesClusterType)
	require.NoError(t, err)
	assert.Equal(t, "kubernetes-release-candidate", components.RayRuntime.Tag)
}

func TestProviderDoesNotFallBackFromFullClusterVersion(t *testing.T) {
	provider := NewProvider(&reader{profiles: []v1.ClusterProfile{profile("v1.2.0", v1.SSHClusterType, "stable")}})

	_, err := provider.ProfileFor("v1.2.0-alpha.1", v1.SSHClusterType)
	require.ErrorContains(t, err, "cluster profile v1.2.0-alpha.1/ssh not found")
}

func TestProviderDoesNotFallBackAcrossClusterTypes(t *testing.T) {
	provider := NewProvider(&reader{profiles: []v1.ClusterProfile{profile("v1.2.0", v1.SSHClusterType, "stable")}})

	_, err := provider.ProfileFor("v1.2.0", v1.KubernetesClusterType)
	require.ErrorContains(t, err, "cluster profile v1.2.0/kubernetes not found")
}

func TestProviderReturnsStorageError(t *testing.T) {
	provider := NewProvider(&reader{err: errors.New("database unavailable")})

	_, err := provider.ProfileFor("v1.2.0", v1.SSHClusterType)
	require.ErrorContains(t, err, "list cluster profiles")
}

type reader struct {
	profiles []v1.ClusterProfile
	err      error
}

func (reader *reader) ListClusterProfile(storage.ListOption) ([]v1.ClusterProfile, error) {
	return reader.profiles, reader.err
}

func profile(name, clusterType, rayRuntimeTag string) v1.ClusterProfile {
	return v1.ClusterProfile{
		Metadata: &v1.Metadata{Name: name},
		Spec: &v1.ClusterProfileSpec{ClusterType: clusterType, Components: v1.ClusterProfileComponents{
			RayRuntime: v1.ImageRef{Tag: rayRuntimeTag},
		}},
	}
}
