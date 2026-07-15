// Package accelerator defines the public extension contract for accelerator plugins.
package accelerator

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	ExternalPluginType = "external"
	InternalPluginType = "internal"
)

type Plugin interface {
	Resource() string
	Type() string
	Handle() PluginHandle
}

type PluginHandle interface {
	GetNodeAccelerator(context.Context, *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error)
	GetNodeRuntimeConfig(context.Context, *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error)
	DetectStaticNodeAccelerator(context.Context, *v1.DetectStaticNodeAcceleratorRequest) (*v1.DetectStaticNodeAcceleratorResponse, error)
	GetContainerRuntimeConfig() (v1.RuntimeConfig, error)
	GetAcceleratorProfile(context.Context) (*v1.AcceleratorProfile, error)
	GetResourceConverter() ResourceConverter
	GetResourceParser() ResourceParser
	Ping(context.Context) error
}

// AcceleratorProfileResolver is an optional plugin capability for node-level
// accelerator types that are aliases of the plugin's registered resource.
type AcceleratorProfileResolver interface {
	GetAcceleratorProfileForType(context.Context, string) (*v1.AcceleratorProfile, bool, error)
}

// StaticNodeRuntimeConfigResolver optionally resolves the cluster runtime
// configuration from a previously detected static-node accelerator status.
// This stays in-process so vendor-specific runtime details are not exposed by
// the public plugin REST API.
type StaticNodeRuntimeConfigResolver interface {
	GetStaticNodeRuntimeConfig(context.Context, *v1.StaticNodeAcceleratorStatus) (*v1.RuntimeConfig, bool, error)
}

// StaticClusterVersionValidator optionally restricts a node-level accelerator
// type to compatible static cluster versions.
type StaticClusterVersionValidator interface {
	ValidateStaticClusterVersion(context.Context, string, string) (bool, error)
}

type ResourceConverter interface {
	ConvertToRay(*v1.ResourceSpec) (*v1.RayResourceSpec, error)
	ConvertToKubernetes(*v1.ResourceSpec) (*v1.KubernetesResourceSpec, error)
}

type ResourceParser interface {
	ParseFromRay(map[string]float64) (*v1.ResourceInfo, error)
	ParseFromKubernetes(map[corev1.ResourceName]resource.Quantity, map[string]string) (*v1.ResourceInfo, error)
}
