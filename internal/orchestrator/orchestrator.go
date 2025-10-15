package orchestrator

import (
	"fmt"
	"strconv"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/cluster"
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
	Cluster            *v1.Cluster
	Storage            storage.Storage
	ImageService       registry.ImageService
	AcceleratorManager accelerator.Manager

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
		clustrManager, err := cluster.NewRaySSHClusterManager(opts.Cluster, imageRegistry, opts.ImageService,
			opts.AcceleratorManager, &command.OSExecutor{}, opts.Storage)
		if err != nil {
			return nil, fmt.Errorf("failed to create ray ssh cluster manager: %w", err)
		}

		return NewRayOrchestrator(RayOptions{
			Options:        opts,
			clusterManager: clustrManager,
		})
	case "kubernetes":
		clusterManager, err := cluster.NewNativeKubernetesCluster(opts.Cluster, imageRegistry, opts.MetricsRemoteWriteURL, opts.Storage)
		if err != nil {
			return nil, fmt.Errorf("failed to create native kubernetes cluster manager: %w", err)
		}
		return newKubernetesOrchestrator(opts, clusterManager)
	default:
		return nil, fmt.Errorf("unsupported cluster type: %s", opts.Cluster.Spec.Type)
	}
}

func getEndpointDeployCluster(s storage.Storage, endpoint *v1.Endpoint) (*v1.Cluster, error) {
	clusterFilter := []storage.Filter{
		{
			Column:   "metadata->name",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, endpoint.Spec.Cluster),
		},
	}

	if endpoint.Metadata.Workspace != "" {
		clusterFilter = append(clusterFilter, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, endpoint.Metadata.Workspace),
		})
	}

	clusterList, err := s.ListCluster(storage.ListOption{Filters: clusterFilter})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list cluster")
	}

	if len(clusterList) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	return &clusterList[0], nil
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
		return nil, storage.ErrResourceNotFound
	}

	return &imageRegistryList[0], nil
}

func getUsedEngine(s storage.Storage, endpoint *v1.Endpoint) (*v1.Engine, error) {
	engine, err := s.ListEngine(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Spec.Engine.Engine),
			},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list engine")
	}

	if len(engine) == 0 {
		return nil, errors.New("engine " + endpoint.Spec.Engine.Engine + " not found")
	}

	if engine[0].Status == nil || engine[0].Status.Phase != v1.EnginePhaseCreated {
		return nil, errors.New("engine " + endpoint.Spec.Engine.Engine + " not ready")
	}

	versionMatched := false

	for _, v := range engine[0].Spec.Versions {
		if v.Version == endpoint.Spec.Engine.Version {
			versionMatched = true
			break
		}
	}

	if !versionMatched {
		return nil, storage.ErrResourceNotFound
	}

	return &engine[0], nil
}

func getEndpointModelRegistry(s storage.Storage, endpoint *v1.Endpoint) (*v1.ModelRegistry, error) {
	modelRegistry, err := s.ListModelRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Spec.Model.Registry),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Metadata.Workspace),
			},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list model registry")
	}

	if len(modelRegistry) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	if modelRegistry[0].Status == nil || modelRegistry[0].Status.Phase != v1.ModelRegistryPhaseCONNECTED {
		return nil, errors.New("model registry " + endpoint.Spec.Model.Registry + " not ready")
	}

	return &modelRegistry[0], nil
}
