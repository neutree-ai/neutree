package resourceparser

import (
	corev1 "k8s.io/api/core/v1"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	NeutreeAcceleratorDevicesAnnotation     = "neutree.ai/accelerator-devices"
	NeutreeAcceleratorAllocationsAnnotation = "neutree.ai/accelerator-allocations"
)

// ResourceParser handles the standard resource semantics for an accelerator.
// Virtualized Kubernetes resources can extend this interface with the optional
// KubernetesVirtualization* parser interfaces below.
type ResourceParser interface {
	ParseFromRay(resource map[string]float64) (*v1.ResourceInfo, error)
	ParseFromKubernetes(resource map[corev1.ResourceName]k8sresource.Quantity, labels map[string]string) (*v1.ResourceInfo, error)
}

type KubernetesNodeResourceContext struct {
	NodeName             string
	AllocatableResources map[corev1.ResourceName]k8sresource.Quantity
	AvailableResources   map[corev1.ResourceName]k8sresource.Quantity
	Labels               map[string]string
	Annotations          map[string]string
	Pods                 []KubernetesPodResourceContext
}

type KubernetesPodResourceContext struct {
	Namespace   string
	Name        string
	UID         string
	NodeName    string
	Labels      map[string]string
	Requests    map[corev1.ResourceName]k8sresource.Quantity
	Limits      map[corev1.ResourceName]k8sresource.Quantity
	Annotations map[string]string
}

type KubernetesEndpointNodeResourceContext struct {
	Name        string
	Labels      map[string]string
	Annotations map[string]string
}

type KubernetesEndpointResourceContext struct {
	EndpointName string
	Namespace    string
	Pods         []KubernetesPodResourceContext
	Nodes        map[string]KubernetesEndpointNodeResourceContext
}

type KubernetesResourceParseResult struct {
	Allocatable         *v1.ResourceInfo
	Available           *v1.ResourceInfo
	Devices             []*v1.DeviceResource
	AcceleratorMetadata map[v1.AcceleratorType]*v1.AcceleratorMetadata
}

// KubernetesVirtualizationResourceParser parses a node only when its labels,
// annotations, and Pod allocations match the parser's virtualization backend.
// The matched flag deliberately separates "not my node" from an empty result.
type KubernetesVirtualizationResourceParser interface {
	ParseKubernetesVirtualizationNode(input KubernetesNodeResourceContext) (*KubernetesResourceParseResult, bool, error)
}

// KubernetesVirtualizationEndpointResourceParser converts backend-specific Pod
// allocation annotations into Neutree Endpoint replica resource semantics.
type KubernetesVirtualizationEndpointResourceParser interface {
	ParseKubernetesVirtualizationEndpoint(input KubernetesEndpointResourceContext) ([]EndpointInstanceResource, bool, error)
}

type EndpointInstanceResource struct {
	InstanceID string
	ReplicaID  string
	NodeID     string
	Devices    []v1.DeviceAllocation
}
