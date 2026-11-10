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

// DependencyDeployMode selects the lifecycle owner for an accelerator's
// cluster components. Provider readiness never changes this value; it is
// determined solely by registered factories and cluster type.
type DependencyDeployMode string

const (
	DependencyDeployModeLegacy   DependencyDeployMode = "legacy"
	DependencyDeployModeOperator DependencyDeployMode = "operator"
)

// KubernetesOperatorFactory creates an accelerator operator for a Kubernetes
// cluster and injects the provider used by that operator.
type KubernetesOperatorFactory func(*v1.Cluster, accelerator.ProfileProvider) (operator.AcceleratorOperator, error)

// StaticClusterOperatorFactory reserves the same extension point for static
// clusters. Static cluster reconciliation does not consume it yet.
type StaticClusterOperatorFactory func(*v1.Cluster) (operator.AcceleratorOperator, error)

type PluginRegistration struct {
	Plugin                       AcceleratorPlugin
	KubernetesOperatorFactory    KubernetesOperatorFactory
	StaticClusterOperatorFactory StaticClusterOperatorFactory
}

type AcceleratorPlugin = accelerator.Plugin

type AcceleratorPluginProvider = accelerator.PluginProvider

type AcceleratorPluginHandle = accelerator.PluginHandle
type Matcher = accelerator.Matcher

// ResourceConverter is the interface for resource converters
// Converts Neutree's unified resource specifications to resource configurations for different cluster types (Ray, Kubernetes)
type ResourceConverter = accelerator.ResourceConverter

var (
	plugins          = make(map[string]AcceleratorPlugin)
	registrations    = make(map[string]PluginRegistration)
	pluginMu         sync.Mutex
	registrationLock sync.RWMutex
)

func registerAcceleratorPlugin(plugin AcceleratorPlugin) {
	pluginMu.Lock()
	plugins[plugin.Resource()] = plugin
	pluginMu.Unlock()

	// Built-in plugins historically registered only their Plugin value. Keep
	// that path intact while making the registration visible to mode lookup.
	_ = RegisterPlugin(PluginRegistration{Plugin: plugin})
}

func GetLocalAcceleratorPlugins() map[string]AcceleratorPlugin {
	return plugins
}

func clusterMatchesAccelerator(cluster *v1.Cluster, resource string) bool {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.Config == nil ||
		cluster.Spec.Config.AcceleratorType == nil || *cluster.Spec.Config.AcceleratorType == "" {
		return true
	}

	return *cluster.Spec.Config.AcceleratorType == resource
}

// RegisterPlugin adds a plugin registration, including optional operator
// factories. A registration with the same resource replaces the existing
// registration only when it refers to the same plugin; this lets a built-in
// plugin attach its factory after its legacy init registration.
func RegisterPlugin(registration PluginRegistration) error {
	if registration.Plugin == nil {
		return fmt.Errorf("accelerator plugin is nil")
	}

	resource := registration.Plugin.Resource()
	if resource == "" {
		return fmt.Errorf("accelerator plugin resource is required")
	}

	registrationLock.Lock()
	defer registrationLock.Unlock()

	if existing, ok := registrations[resource]; ok && existing.Plugin != registration.Plugin {
		return fmt.Errorf("accelerator plugin resource %q is already registered", resource)
	}

	registrations[resource] = registration

	return nil
}

func GetPluginRegistration(acceleratorType string) (PluginRegistration, bool) {
	registrationLock.RLock()
	defer registrationLock.RUnlock()

	registration, ok := registrations[acceleratorType]

	return registration, ok
}

// PluginDependencyDeployMode returns the lifecycle mode selected by plugin
// registration. Missing factories intentionally remain legacy so existing
// plugins keep their profile/config injection path.
func PluginDependencyDeployMode(acceleratorType, clusterType string) DependencyDeployMode {
	registration, ok := GetPluginRegistration(acceleratorType)
	if !ok {
		return DependencyDeployModeLegacy
	}

	switch clusterType {
	case v1.KubernetesClusterType:
		if registration.KubernetesOperatorFactory != nil {
			return DependencyDeployModeOperator
		}
	case v1.SSHClusterType:
		if registration.StaticClusterOperatorFactory != nil {
			return DependencyDeployModeOperator
		}
	}

	return DependencyDeployModeLegacy
}
