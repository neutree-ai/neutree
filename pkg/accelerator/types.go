// Package accelerator defines the public extension contract for accelerator plugins.
package accelerator

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

type PluginProvider interface {
	SupportPlugins() []string
	GetPlugin(string) (Plugin, bool)
}

type Matcher interface {
	MatchCluster(context.Context, *OperatorContext) (bool, error)
}

// OperatorFactory is implemented by plugins that provide a Kubernetes
// accelerator operator. Plugins without this optional interface stay Legacy.
type OperatorFactory interface {
	Matcher
	NewKubernetesOperator(*v1.Cluster, PluginProvider) (AcceleratorOperator, error)
}

type OperatorContext struct {
	Cluster   *v1.Cluster
	Client    client.Client
	Namespace string
}

type AcceleratorOperator interface {
	Name() string
	AcceleratorType() string
	Reconcile(context.Context, *OperatorContext) error
	Status(context.Context, *OperatorContext) error
	Delete(context.Context, *OperatorContext) error
}

// IsOperatorSupported checks plugin capability and cluster matching without creating an Operator.
func IsOperatorSupported(ctx context.Context, plugin Plugin, opCtx *OperatorContext) (bool, error) {
	factory, ok := plugin.(OperatorFactory)
	if !ok {
		return false, nil
	}

	return factory.MatchCluster(ctx, opCtx)
}

func ResolveOperator(ctx context.Context, plugin Plugin, opCtx *OperatorContext, provider PluginProvider) (AcceleratorOperator, error) {
	factory, ok := plugin.(OperatorFactory)
	if !ok {
		return nil, nil
	}

	matched, err := IsOperatorSupported(ctx, plugin, opCtx)
	if err != nil || !matched {
		return nil, err
	}

	return factory.NewKubernetesOperator(opCtx.Cluster, provider)
}

// AcceleratorProfileInput identifies the accelerator profile and optional
// Kubernetes cluster context used to resolve backend-specific runtime fields.
type AcceleratorProfileInput struct {
	AcceleratorType string
	Cluster         *v1.Cluster
}

type PluginHandle interface {
	GetNodeAccelerator(context.Context, *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error)
	GetNodeRuntimeConfig(context.Context, *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error)
	DetectStaticNodeAccelerator(context.Context, *v1.DetectStaticNodeAcceleratorRequest) (*v1.DetectStaticNodeAcceleratorResponse, error)
	GetContainerRuntimeConfig() (v1.RuntimeConfig, error)
	GetAcceleratorProfile(context.Context, AcceleratorProfileInput) (*v1.AcceleratorProfile, error)
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
