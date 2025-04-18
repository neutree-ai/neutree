package orchestrator

import (
	"fmt"

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
}

type Options struct {
	Cluster       *v1.Cluster
	ImageRegistry *v1.ImageRegistry
	ImageService  registry.ImageService

	MetricsRemoteWriteURL string
	Storage               storage.Storage
}

type NewOrchestratorFunc func(opts Options) (Orchestrator, error)

var (
	NewOrchestrator NewOrchestratorFunc = newOrchestrator
)

func newOrchestrator(opts Options) (Orchestrator, error) {
	switch opts.Cluster.Spec.Type {
	case "ssh":
		clustrManager, err := cluster.NewRaySSHClusterManager(opts.Cluster, opts.ImageRegistry, opts.ImageService, &command.OSExecutor{})
		if err != nil {
			return nil, fmt.Errorf("failed to create ray ssh cluster manager: %w", err)
		}
		return NewRayOrchestrator(opts, clustrManager)
	case "kubernetes":
		clusterManager, err := cluster.NewKubeRayClusterManager(opts.MetricsRemoteWriteURL, opts.Cluster, opts.ImageRegistry, opts.ImageService)
		if err != nil {
			return nil, fmt.Errorf("failed to create kube ray cluster manager: %w", err)
		}
		return NewRayOrchestrator(opts, clusterManager)
	default:
		return nil, fmt.Errorf("unsupported cluster type: %s", opts.Cluster.Spec.Type)
	}
}
