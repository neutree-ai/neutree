package v1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type ResourceParser interface {
	// ParseFromRay parses Ray resource configuration to Neutree's unified resource specification
	ParseFromRay(resource map[string]float64) (*ResourceInfo, error)

	// ParseFromKubernetes parses Kubernetes resource configuration to Neutree's unified resource specification
	ParseFromKubernetes(resource map[corev1.ResourceName]resource.Quantity, labels map[string]string) (*ResourceInfo, error)
}
