package plugin

import (
	"context"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
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

type AcceleratorPluginHandle interface {
	GetNodeAccelerator(ctx context.Context, request *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error)
	GetNodeRuntimeConfig(ctx context.Context, request *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error)
	GetSupportEngines(ctx context.Context) (*v1.GetSupportEnginesResponse, error)
	Ping(ctx context.Context) error
	// GetResourceConverter returns the resource converter
	GetResourceConverter() ResourceConverter

	// GetResourceParser returns the resource parser
	GetResourceParser() ResourceParser
}

// ResourceConverter is the interface for resource converters
// Converts Neutree's unified resource specifications to resource configurations for different cluster types (Ray, Kubernetes)
type ResourceConverter interface {
	// ConvertToRay converts to Ray resource configuration
	ConvertToRay(spec *v1.ResourceSpec) (*v1.RayResourceSpec, error)

	// ConvertToKubernetes converts to Kubernetes resource configuration
	ConvertToKubernetes(spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error)
}

type ResourceParser interface {
	// ParseFromRay parses Ray resource configuration to Neutree's unified resource specification
	ParseFromRay(resource map[string]float64) (*v1.ResourceInfo, error)

	// ParseFromKubernetes parses Kubernetes resource configuration to Neutree's unified resource specification
	ParseFromKubernetes(resource map[corev1.ResourceName]resource.Quantity, labels map[string]string) (*v1.ResourceInfo, error)
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
