package cluster

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type Options struct {
	Executor              command.Executor
	Cluster               *v1.Cluster
	ImageService          registry.ImageService
	AcceleratorManager    accelerator.Manager
	Storage               storage.Storage
	MetricsRemoteWriteURL string
}

func NewReconciler(opts Options) (ClusterReconcile, error) {
	switch opts.Cluster.Spec.Type {
	case v1.SSHClusterType:
		return newRaySSHClusterReconcile(opts)
	case v1.KubernetesClusterType:
		return NewKubeRayClusterReconciler(opts)
	default:
		return nil, nil
	}
}
