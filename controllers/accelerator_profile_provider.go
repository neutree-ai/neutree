package controllers

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type AcceleratorProfileGetter interface {
	GetAcceleratorProfile(ctx context.Context, acceleratorType string) (*v1.AcceleratorProfile, bool, error)
}

type AcceleratorManagerProfileProvider struct {
	Manager AcceleratorProfileGetter
}

func NewAcceleratorManagerProfileProvider(manager AcceleratorProfileGetter) *AcceleratorManagerProfileProvider {
	return &AcceleratorManagerProfileProvider{Manager: manager}
}

func (p *AcceleratorManagerProfileProvider) GetAcceleratorProfiles(
	ctx context.Context,
	acceleratorTypes []string,
) (map[string]*v1.AcceleratorProfile, error) {
	profiles := map[string]*v1.AcceleratorProfile{}
	if p == nil || p.Manager == nil {
		return profiles, nil
	}

	for _, acceleratorType := range acceleratorTypes {
		if acceleratorType == "" {
			continue
		}

		profile, supported, err := p.Manager.GetAcceleratorProfile(ctx, acceleratorType)
		if err != nil {
			return nil, err
		}

		if !supported || profile == nil {
			continue
		}

		profiles[acceleratorType] = profile
	}

	return profiles, nil
}
