package plugin

import (
	"fmt"
	"sync"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/operator"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

const (
	BytesPerGiB = 1024 * 1024 * 1024
)

const (
	InternalPluginType = accelerator.InternalPluginType
)

type AcceleratorPlugin = accelerator.Plugin

type AcceleratorPluginProvider interface {
	SupportPlugins() []string
	GetPlugin(acceleratorType string) (AcceleratorPlugin, bool)
}

type AcceleratorPluginHandle = accelerator.PluginHandle

type KubernetesOperatorFactory func(*v1.Cluster, accelerator.PluginProvider) (operator.AcceleratorOperator, error)

type PluginRegistration struct {
	Plugin                    AcceleratorPlugin
	KubernetesOperatorFactory KubernetesOperatorFactory
}

// ResourceConverter is the interface for resource converters
// Converts Neutree's unified resource specifications to resource configurations for different cluster types (Ray, Kubernetes)
type ResourceConverter = accelerator.ResourceConverter

var (
	plugins       = make(map[string]AcceleratorPlugin)
	registrations = make(map[string]PluginRegistration)
	pluginMu      sync.Mutex
)

func registerAcceleratorPlugin(plugin AcceleratorPlugin) {
	pluginMu.Lock()
	defer pluginMu.Unlock()

	plugins[plugin.Resource()] = plugin
	registrations[plugin.Resource()] = PluginRegistration{Plugin: plugin}
}

func RegisterPlugin(registration PluginRegistration) error {
	if registration.Plugin == nil || registration.Plugin.Resource() == "" {
		return fmt.Errorf("accelerator plugin resource is required")
	}
	pluginMu.Lock()
	defer pluginMu.Unlock()
	if existing, ok := registrations[registration.Plugin.Resource()]; ok && existing.Plugin != registration.Plugin {
		return fmt.Errorf("accelerator plugin resource %q is already registered", registration.Plugin.Resource())
	}
	registrations[registration.Plugin.Resource()] = registration
	plugins[registration.Plugin.Resource()] = registration.Plugin
	return nil
}

func GetPluginRegistration(acceleratorType string) (PluginRegistration, bool) {
	pluginMu.Lock()
	defer pluginMu.Unlock()
	registration, ok := registrations[acceleratorType]
	return registration, ok
}

func GetLocalAcceleratorPlugins() map[string]AcceleratorPlugin {
	return plugins
}
