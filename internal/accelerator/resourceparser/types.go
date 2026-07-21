package resourceparser

import (
	corev1 "k8s.io/api/core/v1"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"

	"github.com/neutree-ai/neutree/pkg/accelerator"
)

const (
	NeutreeAcceleratorDevicesAnnotation     = "neutree.ai/accelerator-devices"
	NeutreeAcceleratorAllocationsAnnotation = "neutree.ai/accelerator-allocations"
)

// ResourceParser handles standard accelerator resource semantics. Kubernetes
// Neutree node/pod annotation aggregation lives in internal/resource.
type ResourceParser = accelerator.ResourceParser

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
