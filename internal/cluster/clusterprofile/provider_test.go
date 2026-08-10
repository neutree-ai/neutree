package clusterprofile

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestProviderResolvesExactClusterProfileVersion(t *testing.T) {
	provider := NewProvider(&reader{profiles: []v1.ClusterProfile{
		profile("v1.2.0", "stable"),
		profile("v1.2.0-rc.1", "release-candidate"),
	}})

	resolved, err := provider.ProfileFor("v1.2.0-rc.1")
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0-rc.1", resolved.GetName())

	components, err := provider.ComponentsFor("v1.2.0-rc.1")
	require.NoError(t, err)
	assert.Equal(t, "release-candidate", components.RayRuntime.Tag)
}

func TestProviderDoesNotFallBackFromFullClusterVersion(t *testing.T) {
	provider := NewProvider(&reader{profiles: []v1.ClusterProfile{profile("v1.2.0", "stable")}})

	_, err := provider.ProfileFor("v1.2.0-alpha.1")
	require.ErrorContains(t, err, "cluster profile v1.2.0-alpha.1 not found")
}

func TestProviderReturnsStorageError(t *testing.T) {
	provider := NewProvider(&reader{err: errors.New("database unavailable")})

	_, err := provider.ProfileFor("v1.2.0")
	require.ErrorContains(t, err, "list cluster profiles")
}

type reader struct {
	profiles []v1.ClusterProfile
	err      error
}

func (reader *reader) ListClusterProfile(storage.ListOption) ([]v1.ClusterProfile, error) {
	return reader.profiles, reader.err
}

func profile(name, rayRuntimeTag string) v1.ClusterProfile {
	return v1.ClusterProfile{
		Metadata: &v1.Metadata{Name: name},
		Spec: &v1.ClusterProfileSpec{Components: v1.ClusterProfileComponents{
			RayRuntime: v1.ImageRef{Tag: rayRuntimeTag},
		}},
	}
}
