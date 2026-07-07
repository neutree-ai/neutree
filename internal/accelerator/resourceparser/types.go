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

// ResourceParser handles standard accelerator resource semantics. Kubernetes
// Neutree node/pod annotation aggregation lives in internal/resource.
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

type KubernetesEndpointResourceContext struct {
	EndpointName string
	Namespace    string
	Pods         []KubernetesPodResourceContext
}
