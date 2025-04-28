package orchestrator

import (
	"fmt"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Orchestrator defines the core interface for cluster orchestration
type Orchestrator interface {
	CreateCluster() (string, error)
	DeleteCluster() error
	StartNode(nodeIP string) error
	StopNode(nodeIP string) error
	GetDesireStaticWorkersIP() []string
	HealthCheck() error
	ClusterStatus() (*v1.RayClusterStatus, error)
	ListNodes() ([]v1.NodeSummary, error)

	CreateEndpoint(endpoint *v1.Endpoint) (*v1.EndpointStatus, error)
	DeleteEndpoint(endpoint *v1.Endpoint) error
	GetEndpointStatus(endpoint *v1.Endpoint) (*v1.EndpointStatus, error)
}

type Options struct {
	Cluster      *v1.Cluster
	Storage      storage.Storage
	ImageService registry.ImageService
}

type NewOrchestratorFunc func(opts Options) (Orchestrator, error)

var (
	NewOrchestrator NewOrchestratorFunc = newOrchestrator
)

func newOrchestrator(opts Options) (Orchestrator, error) {
	imageRegistry, err := getRelateImageRegistry(opts.Storage, opts.Cluster)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get relate image registry")
	}

	switch opts.Cluster.Spec.Type {
	case "ssh":
		return NewRayOrchestrator(RayOptions{
			Options:       opts,
			ImageRegistry: imageRegistry,
		})
	default:
		return nil, fmt.Errorf("unsupported cluster type: %s", opts.Cluster.Spec.Type)
	}
}

func getRelateImageRegistry(s storage.Storage, cluster *v1.Cluster) (*v1.ImageRegistry, error) {
	imageRegistryFilter := []storage.Filter{
		{
			Column:   "metadata->name",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Spec.ImageRegistry),
		},
	}

	if cluster.Metadata.Workspace != "" {
		imageRegistryFilter = append(imageRegistryFilter, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Metadata.Workspace),
		})
	}

	imageRegistryList, err := s.ListImageRegistry(storage.ListOption{Filters: imageRegistryFilter})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list image registry")
	}

	if len(imageRegistryList) == 0 {
		return nil, errors.New("relate image registry not found")
	}

	return &imageRegistryList[0], nil
}
