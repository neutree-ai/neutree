package plugin

import (
	"context"

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

func (p *acceleratorRestPlugin) ResolveClusterVirtualizationConfig(
	context.Context,
	*v1.Cluster,
) (*VirtualizationConfig, error) {
	return NewUnsupportedVirtualizationConfig(p.resource), nil
}
