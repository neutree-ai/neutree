package plugin

import (
	"context"
	"sync"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

const (
	BytesPerGiB = 1024 * 1024 * 1024
)

const (
	ExternalPluginType = accelerator.ExternalPluginType
	InternalPluginType = accelerator.InternalPluginType
)

type AcceleratorPlugin = accelerator.Plugin

type AcceleratorPluginProvider interface {
	SupportPlugins() []string
	GetPlugin(acceleratorType string) (AcceleratorPlugin, bool)
}

type ClusterVirtualizationConfigProvider interface {
	ResolveClusterVirtualizationConfig(ctx context.Context, cluster *v1.Cluster) (*VirtualizationConfig, error)
}

type AcceleratorPluginHandle = accelerator.PluginHandle

// ResourceConverter is the interface for resource converters
// Converts Neutree's unified resource specifications to resource configurations for different cluster types (Ray, Kubernetes)
type ResourceConverter = accelerator.ResourceConverter

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
