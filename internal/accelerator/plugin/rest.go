package plugin

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
