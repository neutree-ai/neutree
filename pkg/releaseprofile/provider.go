package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// ComponentProvider resolves the exact component matrix for one cluster
// version and type.
type ComponentProvider interface {
	ComponentsFor(clusterVersion, clusterType string) (v1.ClusterProfileComponents, error)
}

// Provider resolves persisted ReleaseInfo records and exact component matrices.
type Provider struct {
	releaseInfoStorage    storage.ReleaseInfoStorage
	clusterProfileStorage storage.ClusterProfileStorage
}

// NewProvider creates a Provider with the storage required for both current
// policy and exact component resolution.
func NewProvider(
	releaseInfoStorage storage.ReleaseInfoStorage,
	clusterProfileStorage storage.ClusterProfileStorage,
) *Provider {
	return &Provider{
		releaseInfoStorage:    releaseInfoStorage,
		clusterProfileStorage: clusterProfileStorage,
	}
}

// Current returns the ReleaseInfo selected for the running control plane.
func (provider *Provider) Current() (*v1.ReleaseInfo, error) {
	infos, err := provider.releaseInfoStorage.ListReleaseInfo()
	if err != nil {
		return nil, fmt.Errorf("list release infos: %w", err)
	}

	baseline := NewBuilder().CurrentReleaseInfoBaseline()

	for index := range infos {
		if infos[index].GetName() != baseline {
			continue
		}

		return cloneReleaseInfo(&infos[index]), nil
	}

	return nil, fmt.Errorf("release info %s not found", baseline)
}

// ProfileFor returns an exact Profile. It never falls back to a minor line or
// another cluster type.
func (provider *Provider) ProfileFor(clusterVersion string) (*v1.ClusterProfile, error) {
	if _, err := parseExactVPrefixedSemVer(clusterVersion); err != nil {
		return nil, fmt.Errorf("invalid cluster version %q: %w", clusterVersion, err)
	}

	profiles, err := provider.clusterProfileStorage.ListClusterProfile(storage.ListOption{})
	if err != nil {
		return nil, fmt.Errorf("list cluster profiles: %w", err)
	}

	for index := range profiles {
		if profiles[index].GetName() != clusterVersion {
			continue
		}

		return cloneClusterProfile(&profiles[index]), nil
	}

	return nil, fmt.Errorf("cluster profile %s not found", clusterVersion)
}

// ComponentsFor returns the matrix for one supported cluster family.
func (provider *Provider) ComponentsFor(clusterVersion, clusterType string) (v1.ClusterProfileComponents, error) {
	if !v1.IsSupportedClusterType(clusterType) {
		return v1.ClusterProfileComponents{}, fmt.Errorf("unsupported cluster profile type %q", clusterType)
	}

	profile, err := provider.ProfileFor(clusterVersion)
	if err != nil {
		return v1.ClusterProfileComponents{}, err
	}

	components, found := profile.Spec.ComponentsFor(clusterType)
	if !found {
		return v1.ClusterProfileComponents{}, fmt.Errorf("cluster profile %s has no %s component matrix", clusterVersion, clusterType)
	}

	return components, nil
}
