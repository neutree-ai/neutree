package orchestrator

import (
	"fmt"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/cluster"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Orchestrator defines the core interface for cluster orchestration
type Orchestrator interface {
	CreateCluster() (string, error)
	DeleteCluster() error
	SyncCluster() error
	StartNode(nodeIP string) error
	StopNode(nodeIP string) error
	GetDesireStaticWorkersIP() []string
	HealthCheck() error
	ClusterStatus() (*v1.RayClusterStatus, error)
	ListNodes() ([]v1.NodeSummary, error)

	CreateEndpoint(endpoint *v1.Endpoint) (*v1.EndpointStatus, error)
	DeleteEndpoint(endpoint *v1.Endpoint) error
	GetEndpointStatus(endpoint *v1.Endpoint) (*v1.EndpointStatus, error)

	ConnectEndpointModel(endpoint *v1.Endpoint) error
	DisconnectEndpointModel(endpoint *v1.Endpoint) error
}

type Options struct {
	Cluster      *v1.Cluster
	Storage      storage.Storage
	ImageService registry.ImageService

	MetricsRemoteWriteURL string
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
		clustrManager, err := cluster.NewRaySSHClusterManager(opts.Cluster, imageRegistry, opts.ImageService, &command.OSExecutor{})
		if err != nil {
			return nil, fmt.Errorf("failed to create ray ssh cluster manager: %w", err)
		}

		return NewRayOrchestrator(RayOptions{
			Options:        opts,
			clusterManager: clustrManager,
		})
	case "kubernetes":
		clusterManager, err := cluster.NewKubeRayClusterManager(opts.Cluster, imageRegistry, opts.ImageService, opts.MetricsRemoteWriteURL)
		if err != nil {
			return nil, fmt.Errorf("failed to create kube ray cluster manager: %w", err)
		}

		return NewRayOrchestrator(RayOptions{
			Options:        opts,
			clusterManager: clusterManager,
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
