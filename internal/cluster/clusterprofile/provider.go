package clusterprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Reader is the read-only storage boundary used by runtime ClusterProfile
// consumers.
type Reader interface {
	ListClusterProfile(option storage.ListOption) ([]v1.ClusterProfile, error)
}

// Provider resolves a ClusterProfile by its complete Cluster version and type.
type Provider struct {
	reader Reader
}

// NewProvider creates a ClusterProfile provider.
func NewProvider(reader Reader) *Provider {
	return &Provider{reader: reader}
}

// ProfileFor returns the exact full-version ClusterProfile for one cluster
// family. It deliberately does not fall back to a baseline, minor, or another
// cluster type.
func (provider *Provider) ProfileFor(clusterVersion, clusterType string) (*v1.ClusterProfile, error) {
	if !v1.IsSupportedClusterType(clusterType) {
		return nil, fmt.Errorf("unsupported cluster profile type %q", clusterType)
	}

	profiles, err := provider.reader.ListClusterProfile(storage.ListOption{})
	if err != nil {
		return nil, fmt.Errorf("list cluster profiles: %w", err)
	}

	for index := range profiles {
		if profiles[index].GetName() == clusterVersion && profiles[index].GetClusterType() == clusterType {
			return &profiles[index], nil
		}
	}

	return nil, fmt.Errorf("cluster profile %s/%s not found", clusterVersion, clusterType)
}

// ComponentsFor returns the typed component image profile for one complete
// Cluster version.
func (provider *Provider) ComponentsFor(clusterVersion, clusterType string) (v1.ClusterProfileComponents, error) {
	profile, err := provider.ProfileFor(clusterVersion, clusterType)
	if err != nil {
		return v1.ClusterProfileComponents{}, err
	}

	if profile.Spec == nil {
		return v1.ClusterProfileComponents{}, fmt.Errorf("cluster profile %s/%s has no spec", clusterVersion, clusterType)
	}

	return profile.Spec.Components, nil
}
