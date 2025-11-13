package resource

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
)

// Manager provides resource management capabilities including cluster resource
// collection and resource format conversion.
type Manager interface {
	// Cluster returns the cluster resource service.
	Cluster() ClusterResourceService

	// Converter returns the resource converter service.
	Converter() ResourceConverter
}

// ClusterResourceService provides cluster-level resource management.
type ClusterResourceService interface {
	// CollectClusterResources collects all resources from a cluster including
	// CPU, memory, and accelerator resources.
	CollectClusterResources(ctx context.Context, cluster *v1.Cluster) (*v1.ClusterResources, error)
}

// ResourceConverter converts resource specifications to platform-specific formats.
type ResourceConverter interface {
	// ConvertToRay converts ResourceSpec to Ray resource format.
	ConvertToRay(ctx context.Context, spec *v1.ResourceSpec) (*v1.RayResourceSpec, error)

	// ConvertToKubernetes converts ResourceSpec to Kubernetes resource format.
	ConvertToKubernetes(ctx context.Context, spec *v1.ResourceSpec) (*v1.KubernetesResourceSpec, error)
}

// Config contains configuration for resource Manager.
type Config struct {
	PluginRegistry accelerator.PluginRegistry
}

// NewManager creates a new resource Manager.
func NewManager(cfg *Config) Manager {
	converterMgr := newConverter(cfg.PluginRegistry)

	clusterSvc := &clusterService{
		acceleratorPluginRegistry: cfg.PluginRegistry,
	}

	return &manager{
		acceleratorPluginRegistry: cfg.PluginRegistry,
		converterMgr:              converterMgr,
		clusterSvc:                clusterSvc,
	}
}

type manager struct {
	acceleratorPluginRegistry accelerator.PluginRegistry
	converterMgr              *converter
	clusterSvc                *clusterService
}

func (m *manager) Cluster() ClusterResourceService {
	return m.clusterSvc
}

func (m *manager) Converter() ResourceConverter {
	return m.converterMgr
}
