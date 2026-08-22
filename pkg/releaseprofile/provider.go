package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ReleaseInfoReader is the read-only storage boundary used to resolve the
// current ReleaseInfo policy.
type ReleaseInfoReader interface {
	ListReleaseInfo() ([]v1.ReleaseInfo, error)
}

// ClusterProfileReader is the read-only storage boundary used to resolve exact
// ClusterProfiles. It deliberately has no dependency on the storage package.
type ClusterProfileReader interface {
	ListClusterProfiles() ([]v1.ClusterProfile, error)
}

// ClusterProfileReaderFunc adapts a storage query at the composition root to
// the narrow ClusterProfileReader boundary.
type ClusterProfileReaderFunc func() ([]v1.ClusterProfile, error)

func (reader ClusterProfileReaderFunc) ListClusterProfiles() ([]v1.ClusterProfile, error) {
	return reader()
}

// Provider resolves the current ReleaseInfo and exact ClusterProfile matrices.
// The dedicated constructors keep callers dependent only on the reader each
// operation requires.
type Provider struct {
	releaseInfoReader    ReleaseInfoReader
	clusterProfileReader ClusterProfileReader
	buildIdentity        string
}

// NewReleaseInfoProvider creates a Provider that can resolve Current.
func NewReleaseInfoProvider(reader ReleaseInfoReader, buildIdentity string) *Provider {
	return &Provider{releaseInfoReader: reader, buildIdentity: buildIdentity}
}

// NewClusterProfileProvider creates a Provider that can resolve exact Profiles
// and component matrices.
func NewClusterProfileProvider(reader ClusterProfileReader) *Provider {
	return &Provider{clusterProfileReader: reader}
}

// Current returns the database record selected by the control-plane baseline.
func (provider *Provider) Current() (*v1.ReleaseInfo, error) {
	if provider == nil || provider.releaseInfoReader == nil {
		return nil, fmt.Errorf("release info reader is required")
	}

	infos, err := provider.releaseInfoReader.ListReleaseInfo()
	if err != nil {
		return nil, fmt.Errorf("list release infos: %w", err)
	}

	baseline, err := ResolveCurrentControlPlaneBaseline(provider.buildIdentity, infos)
	if err != nil {
		return nil, err
	}

	for index := range infos {
		if infos[index].GetName() == baseline {
			return &infos[index], nil
		}
	}

	return nil, fmt.Errorf("release info %s not found", baseline)
}

// ProfileFor returns the exact full-version ClusterProfile. It deliberately
// does not fall back to a baseline or minor version.
func (provider *Provider) ProfileFor(clusterVersion string) (*v1.ClusterProfile, error) {
	if provider == nil || provider.clusterProfileReader == nil {
		return nil, fmt.Errorf("cluster profile reader is required")
	}

	if _, err := NormalizeClusterMinor(clusterVersion); err != nil {
		return nil, fmt.Errorf("invalid cluster version %q: %w", clusterVersion, err)
	}

	profiles, err := provider.clusterProfileReader.ListClusterProfiles()
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
