// Package accelerator defines the public extension contract for accelerator plugins.
package accelerator

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

// RuntimeProfileProvider is an optional plugin capability for accelerator
// families that require a runtime configuration selected by an opaque profile.
type RuntimeProfileProvider interface {
	GetRuntimeConfigForProfile(context.Context, string) (v1.RuntimeConfig, error)
}

type ResourceConverter interface {
	ConvertToRay(*v1.ResourceSpec) (*v1.RayResourceSpec, error)
	ConvertToKubernetes(*v1.ResourceSpec) (*v1.KubernetesResourceSpec, error)
}

type ResourceParser interface {
	ParseFromRay(map[string]float64) (*v1.ResourceInfo, error)
	ParseFromKubernetes(map[corev1.ResourceName]resource.Quantity, map[string]string) (*v1.ResourceInfo, error)
}
