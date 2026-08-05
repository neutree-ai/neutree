package releaseinfo

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Reader is the read-only storage boundary used by runtime ReleaseInfo consumers.
type Reader interface {
	ListReleaseInfo() ([]v1.ReleaseInfo, error)
}

// Provider resolves the ReleaseInfo row for one control-plane build identity.
type Provider struct {
	reader        Reader
	buildIdentity string
}

func NewProvider(reader Reader, buildIdentity string) *Provider {
	return &Provider{reader: reader, buildIdentity: buildIdentity}
}

// Current returns the database record selected by the control-plane baseline.
func (provider *Provider) Current() (*v1.ReleaseInfo, error) {
	infos, err := provider.reader.ListReleaseInfo()
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

// ClusterVersion returns the selected Cluster version from the current matrix.
func (provider *Provider) ClusterVersion(version string) (*v1.ReleaseInfoClusterVersion, error) {
	info, err := provider.Current()
	if err != nil {
		return nil, err
	}

	for index := range info.Spec.ClusterVersions {
		if info.Spec.ClusterVersions[index].Version == version {
			return &info.Spec.ClusterVersions[index], nil
		}
	}

	return nil, fmt.Errorf("cluster version %s is not supported by release info %s", version, info.GetName())
}
