package cluster

import (
	"context"
	"fmt"
	"sync"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	ImagePullSecretName = "image-pull-secret" //nolint:gosec
)

var (
	ErrorRayNodeNotFound = errors.New("ray node not found")
)

type ClusterReconcile interface {
	Reconcile(ctx context.Context, cluster *v1.Cluster) error
	ReconcileDelete(ctx context.Context, cluster *v1.Cluster) error
}

// ClusterProfileComponentResolver resolves the typed component profile for an
// exact Cluster version.
type ClusterProfileComponentResolver interface {
	ComponentsFor(clusterVersion, clusterType string) (v1.ClusterProfileComponents, error)
}

type ReconcileContext struct {
	Ctx           context.Context
	Cluster       *v1.Cluster
	ImageRegistry *v1.ImageRegistry
	// ProfileComponents is the exact ClusterProfile selected by spec.version.
	ProfileComponents v1.ClusterProfileComponents
	ProfileSelected   bool

	// ssh cluster specific fields
	sshClusterConfig    *v1.RaySSHProvisionClusterConfig
	sshRayClusterConfig *v1.RayClusterConfig
	sshConfigGenerator  *raySSHLocalConfigGenerator
	processMessages     []string
	lock                sync.Mutex

	rayService dashboard.DashboardService

	// kubernetes cluster specific fields
	ctrClient        client.Client
	clusterNamespace string

	// native kubernetes cluster specific fields
	kubernetesClusterConfig *v1.KubernetesClusterConfig

	logger klog.Logger
}

func NewReconcile(cluster *v1.Cluster, acceleratorManager accelerator.Manager,
	s storage.Storage, metricsRemoteWriteURL string) (ClusterReconcile, error) {
	components, err := resolveClusterProfileComponents(cluster, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid cluster version: %w", err)
	}

	return newReconcile(cluster, acceleratorManager, s, metricsRemoteWriteURL, components)
}

func NewReconcileWithClusterProfile(cluster *v1.Cluster, acceleratorManager accelerator.Manager,
	s storage.Storage, metricsRemoteWriteURL string, resolver ClusterProfileComponentResolver) (ClusterReconcile, error) {
	components, err := resolveClusterProfileComponents(cluster, resolver)
	if err != nil {
		return nil, err
	}

	return newReconcile(cluster, acceleratorManager, s, metricsRemoteWriteURL, components)
}

func newReconcile(cluster *v1.Cluster, acceleratorManager accelerator.Manager,
	s storage.Storage, metricsRemoteWriteURL string, components v1.ClusterProfileComponents) (ClusterReconcile, error) {
	switch cluster.Spec.Type {
	case v1.SSHClusterType:
		legacy := &sshRayClusterReconciler{
			executor:           &command.OSExecutor{},
			acceleratorManager: acceleratorManager,
			storage:            s,
			profileComponents:  components,
			profileSelected:    true,
		}

		useStaticFlow, err := isStaticNodeClusterFlowVersion(cluster.GetVersion())
		if err != nil {
			return nil, err
		}

		if useStaticFlow {
			return &staticRayReconciler{
				storage:            s,
				acceleratorManager: acceleratorManager,
				legacy:             legacy,
				profileComponents:  components,
			}, nil
		}

		return legacy, nil
	case v1.KubernetesClusterType:
		return NewNativeKubernetesClusterReconcilerWithClusterProfileComponents(s, acceleratorManager, metricsRemoteWriteURL, components), nil
	default:
		return nil, fmt.Errorf("unsupported cluster type: %s", cluster.Spec.Type)
	}
}

func resolveClusterProfileComponents(
	cluster *v1.Cluster,
	resolver ClusterProfileComponentResolver,
) (v1.ClusterProfileComponents, error) {
	if cluster == nil || cluster.Spec == nil {
		return v1.ClusterProfileComponents{}, fmt.Errorf("cluster spec is required")
	}

	if _, err := releaseinfo.NormalizeClusterMinor(cluster.Spec.Version); err != nil {
		return v1.ClusterProfileComponents{}, fmt.Errorf("invalid cluster version %q: %w", cluster.Spec.Version, err)
	}

	if resolver == nil {
		return v1.ClusterProfileComponents{}, fmt.Errorf("exact cluster profile component resolver is required for cluster version %s", cluster.Spec.Version)
	}

	components, err := resolver.ComponentsFor(cluster.Spec.Version, cluster.Spec.Type)
	if err != nil {
		return v1.ClusterProfileComponents{}, fmt.Errorf("resolve cluster profile components: %w", err)
	}

	return components, nil
}

func profileImage(componentName string, ref v1.ImageRef) (string, error) {
	if ref.Image == "" || ref.Tag == "" {
		return "", fmt.Errorf("cluster profile component %s requires image and tag", componentName)
	}

	return ref.Image + ":" + ref.Tag, nil
}

func isStaticNodeClusterFlowVersion(version string) (bool, error) {
	useStaticNodeFlow, err := semver.LessThan(v1.StaticNodeClusterFlowVersionGate, version)
	if err != nil {
		return false, fmt.Errorf("invalid cluster version %q: %w", version, err)
	}

	return useStaticNodeFlow, nil
}

// IsStaticNodeClusterFlowVersion reports whether a cluster version uses the
// static-node-backed SSH reconciliation flow.
func IsStaticNodeClusterFlowVersion(version string) (bool, error) {
	return isStaticNodeClusterFlowVersion(version)
}
