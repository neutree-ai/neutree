package orchestrator

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Orchestrator defines the core interface for cluster orchestration
type Orchestrator interface {
	CreateEndpoint(endpoint *v1.Endpoint) (*v1.EndpointStatus, error)
	DeleteEndpoint(endpoint *v1.Endpoint) error
	GetEndpointStatus(endpoint *v1.Endpoint) (*v1.EndpointStatus, error)

	ConnectEndpointModel(endpoint *v1.Endpoint) error
	DisconnectEndpointModel(endpoint *v1.Endpoint) error
}

type Options struct {
	Cluster            *v1.Cluster
	Storage            storage.Storage
	AcceleratorManager accelerator.Manager
}

type NewOrchestratorFunc func(opts Options) (Orchestrator, error)

var (
	NewOrchestrator NewOrchestratorFunc = newOrchestrator
)

func newOrchestrator(opts Options) (Orchestrator, error) {
	switch opts.Cluster.Spec.Type {
	case v1.SSHClusterType:
		return NewRayOrchestrator(RayOptions{
			Options: opts,
		}), nil
	case v1.KubernetesClusterType:
		return newKubernetesOrchestrator(opts), nil
	default:
		return nil, fmt.Errorf("unsupported cluster type: %s", opts.Cluster.Spec.Type)
	}
}
