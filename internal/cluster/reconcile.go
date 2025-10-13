package cluster

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	defaultWorkdir             = "/home/ray"
	defaultModelCacheMountPath = defaultWorkdir + "/.neutree/model-cache"

	ImagePullSecretName = "image-pull-secret" //nolint:gosec
)

var (
	ErrorRayNodeNotFound = errors.New("ray node not found")
)

type ClusterReconcile interface {
	Reconcile(ctx context.Context, cluster *v1.Cluster) error
	ReconcileDelete(ctx context.Context, cluster *v1.Cluster) error
}

type ReconcileContext struct {
	Ctx           context.Context
	Cluster       *v1.Cluster
	ImageRegistry *v1.ImageRegistry

	// ssh cluster specific fields
	sshClusterConfig    *v1.RaySSHProvisionClusterConfig
	sshRayClusterConfig *v1.RayClusterConfig
	sshConfigGenerator  *raySSHLocalConfigGenerator

	// kubernetes cluster specific fields
	ctrClient               client.Client
	kubeconfig              string
	clusterNamespace        string
	installObjects          []client.Object
	kubernetesClusterConfig *v1.RayKubernetesProvisionClusterConfig

	rayService dashboard.DashboardService
}

type NewReconciler func(cluster *v1.Cluster, acceleratorManager accelerator.Manager, s storage.Storage, metricsRemoteWriteURL string) (ClusterReconcile, error)

var NewReconcile NewReconciler = newReconcile

func newReconcile(cluster *v1.Cluster, acceleratorManager accelerator.Manager, s storage.Storage, metricsRemoteWriteURL string) (ClusterReconcile, error) {
	switch cluster.Spec.Type {
	case v1.SSHClusterType:
		return newRaySSHClusterReconcile(acceleratorManager, s), nil
	case v1.KubernetesClusterType:
		return NewKubeRayClusterReconciler(acceleratorManager, s, metricsRemoteWriteURL), nil
	default:
		return nil, fmt.Errorf("unsupported cluster type: %s", cluster.Spec.Type)
	}
}
