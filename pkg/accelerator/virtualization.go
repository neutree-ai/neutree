package accelerator

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// VirtualizationNodeScopeLabel selects the nodes managed by an accelerator
// virtualization provider.
type VirtualizationNodeScopeLabel struct {
	Key           string
	EnabledValue  string
	DisabledValue string
}

// DevicePluginTemplate is an optional accelerator-owned Kubernetes manifest.
// Core renders and applies it as part of the shared HAMi lifecycle.
type DevicePluginTemplate struct {
	Manifest string
}

// VirtualizationConfig describes an accelerator provider's cluster-specific
// HAMi ownership, configuration, and virtualization mode.
type VirtualizationConfig struct {
	Supported            bool
	BlockingReasons      []string
	CandidateNodes       []string
	NodeScopeLabel       VirtualizationNodeScopeLabel
	DevicePluginTemplate *DevicePluginTemplate
	ConfigPatch          map[string]interface{}
	// Mode is the effective virtualization mode for the cluster. It is the
	// user-requested mode when supported, otherwise the provider default.
	Mode v1.AcceleratorVirtualizationMode
	// DefaultMode is the provider's default mode when the user sets none.
	DefaultMode v1.AcceleratorVirtualizationMode
	// SupportedModes are the modes this provider accepts.
	SupportedModes []v1.AcceleratorVirtualizationMode
	// SupportedResources are the virtualization.* resource keys legal under the
	// effective mode, e.g. ["virtualization.memory_mib", "virtualization.core_percent"].
	// They MUST be computed from the effective mode, not fixed per provider:
	// different modes allow different resource keys (e.g. template mode omits
	// virtualization.core_percent because compute cores are fixed by the
	// hard-sliced template). A provider implementing more than one mode must
	// branch on cluster.Spec.AcceleratorVirtualization.Mode when building this
	// list.
	SupportedResources []string
}

// ResolveEffectiveMode returns the effective virtualization mode: the requested
// mode when it is supported, the default when nothing was requested. The bool
// reports whether the requested mode was accepted; a false result means the
// request must be rejected before applying.
func ResolveEffectiveMode(
	requested v1.AcceleratorVirtualizationMode,
	defaultMode v1.AcceleratorVirtualizationMode,
	supported []v1.AcceleratorVirtualizationMode,
) (v1.AcceleratorVirtualizationMode, bool) {
	if requested == "" {
		return defaultMode, true
	}

	for _, mode := range supported {
		if mode == requested {
			return requested, true
		}
	}

	return requested, false
}

// ClusterVirtualizationConfigProvider is implemented by accelerator plugins
// that provide virtualization configuration for a Kubernetes cluster.
type ClusterVirtualizationConfigProvider interface {
	ResolveClusterVirtualizationConfig(context.Context, *v1.Cluster) (*VirtualizationConfig, error)
}
