package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ReleaseInfoReader is the narrow read boundary for current policy resolution.
type ReleaseInfoReader interface {
	ListReleaseInfo() ([]v1.ReleaseInfo, error)
}

// ClusterProfileReader is the narrow read boundary for exact Profile
// resolution. It intentionally does not depend on pkg/storage.
type ClusterProfileReader interface {
	ListClusterProfile() ([]v1.ClusterProfile, error)
}

// ClusterProfileReaderFunc adapts a composition-root query to the narrow
// ClusterProfileReader boundary.
type ClusterProfileReaderFunc func() ([]v1.ClusterProfile, error)

func (reader ClusterProfileReaderFunc) ListClusterProfile() ([]v1.ClusterProfile, error) {
	return reader()
}

// Provider resolves persisted ReleaseInfo records and exact component matrices.
type Provider struct {
	releaseInfoReader    ReleaseInfoReader
	clusterProfileReader ClusterProfileReader
	buildIdentity        string
}

// NewReleaseInfoProvider creates a Provider for Current policy resolution.
func NewReleaseInfoProvider(reader ReleaseInfoReader, buildIdentity string) *Provider {
	return &Provider{releaseInfoReader: reader, buildIdentity: buildIdentity}
}

// NewClusterProfileProvider creates a Provider for exact Profile resolution.
func NewClusterProfileProvider(reader ClusterProfileReader) *Provider {
	return &Provider{clusterProfileReader: reader}
}

// Current returns the ReleaseInfo selected for the running control plane.
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
		if infos[index].GetName() != baseline {
			continue
		}

		if err := ValidateReleaseInfo(&infos[index]); err != nil {
			return nil, fmt.Errorf("invalid current release info %q: %w", baseline, err)
		}

		return cloneReleaseInfo(&infos[index]), nil
	}

	return nil, fmt.Errorf("release info %s not found", baseline)
}

// ProfileFor returns an exact Profile. It never falls back to a minor line or
// another cluster type.
func (provider *Provider) ProfileFor(clusterVersion string) (*v1.ClusterProfile, error) {
	if provider == nil || provider.clusterProfileReader == nil {
		return nil, fmt.Errorf("cluster profile reader is required")
	}

	if _, err := parseExactVPrefixedSemVer(clusterVersion); err != nil {
		return nil, fmt.Errorf("invalid cluster version %q: %w", clusterVersion, err)
	}

	profiles, err := provider.clusterProfileReader.ListClusterProfile()
	if err != nil {
		return nil, fmt.Errorf("list cluster profiles: %w", err)
	}

	for index := range profiles {
		if profiles[index].GetName() != clusterVersion {
			continue
		}

		if err := ValidateClusterProfile(&profiles[index]); err != nil {
			return nil, fmt.Errorf("invalid cluster profile %q: %w", clusterVersion, err)
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
