package plugin

import (
	"context"
	"sync"

	v1 "github.com/neutree-ai/neutree/api/v1"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
)

const (
	BytesPerGiB = 1024 * 1024 * 1024
)

const (
	ExternalPluginType = "external"
	InternalPluginType = "internal"
)

type AcceleratorPlugin interface {
	Handle() AcceleratorPluginHandle
	// Resource returns the accelerator resource type identifier (e.g., "nvidia_gpu", "amd_gpu")
	// This identifier is used for:
	// - Plugin registration and lookup
	// - Resource converter registration (maps to accelerator.type in user configuration)
	Resource() string
	Type() string
}

type AcceleratorPluginProvider interface {
	SupportPlugins() []string
	GetPlugin(acceleratorType string) (AcceleratorPlugin, bool)
}

type ClusterVirtualizationConfigProvider interface {
	ResolveClusterVirtualizationConfig(ctx context.Context, cluster *v1.Cluster) (*VirtualizationConfig, error)
}

type AcceleratorPluginHandle interface {
	GetNodeAccelerator(ctx context.Context, request *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error)
	GetNodeRuntimeConfig(ctx context.Context, request *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error)
	Ping(ctx context.Context) error
	// GetResourceConverter returns the resource converter
	GetResourceConverter() ResourceConverter

	// GetResourceParser returns the resource parser
	GetResourceParser() resourceview.ResourceParser

	// GetContainerRuntimeConfig returns the static RuntimeConfig for engine containers.
	// Unlike GetNodeRuntimeConfig, this does NOT require SSH access to a node.
	// Used to generate Docker run_options for engine containers (runtime_env.container).
	GetContainerRuntimeConfig() (v1.RuntimeConfig, error)
}

// ResourceConverter is the interface for resource converters
// Converts Neutree's unified resource specifications to resource configurations for different cluster types (Ray, Kubernetes)
type ResourceConverter interface {
	// ConvertToRay converts to Ray resource configuration
	ConvertToRay(spec *v1.ResourceSpec) (*v1.RayResourceSpec, error)

	// ConvertToKubernetes converts to Kubernetes resource configuration
	ConvertToKubernetes(spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error)
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
