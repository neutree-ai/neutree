package plugin

import (
	"context"
	"sync"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	ExternalPluginType = "external"
	InternalPluginType = "internal"
)

type AcceleratorPlugin interface {
	Handle() AcceleratorPluginHandle
	Resource() string
	Type() string
}

type AcceleratorPluginHandle interface {
	GetNodeAccelerator(ctx context.Context, request *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error)
	GetNodeRuntimeConfig(ctx context.Context, request *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error)
	GetKubernetesContainerAccelerator(ctx context.Context, request *v1.GetContainerAcceleratorRequest) (*v1.GetContainerAcceleratorResponse, error)
	GetKubernetesContainerRuntimeConfig(ctx context.Context, request *v1.GetContainerRuntimeConfigRequest) (*v1.GetContainerRuntimeConfigResponse, error)
	GetSupportEngines(ctx context.Context) (*v1.GetSupportEnginesResponse, error)
	Ping(ctx context.Context) error
}

type RegisterHandle func(plugin AcceleratorPlugin)

var (
	plugins  = make(map[string]AcceleratorPlugin)
	pluginMu sync.Mutex
)

func registerAcceleratorPlugin(plugin AcceleratorPlugin) {
	pluginMu.Lock()
	defer pluginMu.Unlock()

	plugins[plugin.Resource()] = plugin
}

func GetLocalAcceleratorPlugins() map[string]AcceleratorPlugin {
	return plugins
}
