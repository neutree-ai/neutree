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
