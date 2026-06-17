package plugin

import (
	"context"
	"net/http"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type acceleratorRestPlugin struct {
	client   AcceleratorPluginHandle
	resource string
}

func NewAcceleratorRestPlugin(resourceName, baseURL string) AcceleratorPlugin {
	return &acceleratorRestPlugin{
		client:   newAcceleratorPluginClient(baseURL),
		resource: resourceName,
	}
}

func (p *acceleratorRestPlugin) Handle() AcceleratorPluginHandle {
	return p.client
}

func (p *acceleratorRestPlugin) Resource() string {
	return p.resource
}

func (p *acceleratorRestPlugin) Type() string {
	return ExternalPluginType
}

func (p *acceleratorRestPlugin) RuntimeProfile(
	ctx context.Context,
	accelerator v1.StaticNodeAcceleratorStatus,
) (*v1.AcceleratorProfile, bool, error) {
	if accelerator.Type != "" && accelerator.Type != p.resource {
		return nil, false, nil
	}

	provider, ok := p.client.(AcceleratorProfileProvider)
	if !ok {
		return nil, false, nil
	}

	profile, err := provider.GetAcceleratorProfile(ctx)
	if IsHTTPStatus(err, http.StatusNotFound) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	if profile == nil {
		return nil, false, nil
	}

	if profile.AcceleratorType == "" {
		copied := *profile
		copied.AcceleratorType = p.resource
		profile = &copied
	}

	return profile, true, nil
}
