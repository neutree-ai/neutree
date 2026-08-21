package clusterprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Reader is the read-only storage boundary used by runtime ClusterProfile
// consumers.
type Reader interface {
	ListClusterProfile(option storage.ListOption) ([]v1.ClusterProfile, error)
}

// Provider resolves an exact ClusterProfile and one of its component matrices.
type Provider struct {
	reader Reader
}

// NewProvider creates a ClusterProfile provider.
func NewProvider(reader Reader) *Provider {
	return &Provider{reader: reader}
}

// ProfileFor returns the exact full-version ClusterProfile. It deliberately
// does not fall back to a baseline or minor version.
func (provider *Provider) ProfileFor(clusterVersion string) (*v1.ClusterProfile, error) {
	if provider == nil || provider.reader == nil {
		return nil, fmt.Errorf("cluster profile reader is required")
	}

	if _, err := releaseinfo.NormalizeClusterMinor(clusterVersion); err != nil {
		return nil, fmt.Errorf("invalid cluster version %q: %w", clusterVersion, err)
	}

	profiles, err := provider.reader.ListClusterProfile(storage.ListOption{})
	if err != nil {
		return nil, fmt.Errorf("list cluster profiles: %w", err)
	}

	for index := range profiles {
		if profiles[index].GetName() == clusterVersion {
			return &profiles[index], nil
		}
	}

	return nil, fmt.Errorf("cluster profile %s not found", clusterVersion)
}

// ComponentsFor returns the typed component image profile for one complete
// Cluster version.
func (provider *Provider) ComponentsFor(clusterVersion, clusterType string) (v1.ClusterProfileComponents, error) {
	if !v1.IsSupportedClusterType(clusterType) {
		return v1.ClusterProfileComponents{}, fmt.Errorf("unsupported cluster profile type %q", clusterType)
	}

	profile, err := provider.ProfileFor(clusterVersion)
	if err != nil {
		return v1.ClusterProfileComponents{}, err
	}

	if profile == nil || profile.Spec == nil {
		return v1.ClusterProfileComponents{}, fmt.Errorf("cluster profile %s has no spec", clusterVersion)
	}

	components, found := profile.Spec.ComponentsFor(clusterType)
	if !found {
		return v1.ClusterProfileComponents{}, fmt.Errorf("cluster profile %s has no %s component matrix", clusterVersion, clusterType)
	}

	return components, nil
}
