package orchestrator

import (
	"fmt"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Orchestrator defines the core interface for cluster orchestration
type Orchestrator interface {
	CreateEndpoint(endpoint *v1.Endpoint) error
	DeleteEndpoint(endpoint *v1.Endpoint) error
	PauseEndpoint(endpoint *v1.Endpoint) error
	GetEndpointStatus(endpoint *v1.Endpoint) (*v1.EndpointStatus, error)
}

type Options struct {
	Cluster        *v1.Cluster
	Storage        storage.Storage
	AcceleratorMgr accelerator.Manager
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

type OrchestratorContext struct {
	Cluster       *v1.Cluster
	Engine        *v1.Engine
	ModelRegistry *v1.ModelRegistry
	ImageRegistry *v1.ImageRegistry
	Endpoint      *v1.Endpoint

	// ray dashboard service
	rayService dashboard.DashboardService

	// kubernetes cluster specific fields
	ctrClient client.Client

	logger klog.Logger
}
