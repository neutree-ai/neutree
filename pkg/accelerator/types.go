// Package accelerator defines the public extension contract for accelerator plugins.
package accelerator

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
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
}

// StaticNodeRuntimeConfigResolver optionally resolves a cluster runtime
// configuration from a previously detected static-node accelerator status.
// This stays in-process so vendor-specific runtime details are not exposed by
// the public plugin REST API.
type StaticNodeRuntimeConfigResolver interface {
	GetStaticNodeRuntimeConfig(context.Context, *v1.StaticNodeAcceleratorStatus) (*v1.RuntimeConfig, bool, error)
}

// ConvertInput carries the context a resource converter needs to translate an
// endpoint's resource spec into Ray or Kubernetes resource terms. The Spec is
// the endpoint's resource request; the Cluster and Endpoint objects provide
// accelerator virtualization context (for example the cluster's effective
// virtualization mode) that a converter may branch on.
type ConvertInput struct {
	Cluster  *v1.Cluster
	Endpoint *v1.Endpoint
	Spec     *v1.ResourceSpec
}

type ResourceConverter interface {
	ConvertToRay(ConvertInput) (*v1.RayResourceSpec, error)
	ConvertToKubernetes(ConvertInput) (*v1.KubernetesResourceSpec, error)
}

type ResourceParser interface {
	ParseFromRay(map[string]float64) (*v1.ResourceInfo, error)
	ParseFromKubernetes(map[corev1.ResourceName]resource.Quantity, map[string]string) (*v1.ResourceInfo, error)
}
