package cluster

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
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

// ReleaseComponentResolver resolves the current control plane's immutable
// component matrix for one Cluster version and accelerator selection.
type ReleaseComponentResolver interface {
	ComponentsFor(clusterVersion, acceleratorType string) (map[string]string, error)
}

// ReleaseInfoProvider extends ReleaseComponentResolver with the matrix identity
// needed to persist and report a controller-owned upgrade snapshot.
type ReleaseInfoProvider interface {
	ReleaseComponentResolver
	Current() (*v1.ReleaseInfo, error)
}

type ReconcileContext struct {
	Ctx           context.Context
	Cluster       *v1.Cluster
	ImageRegistry *v1.ImageRegistry
	// ReleaseComponents is the resolved ReleaseInfo snapshot used by v1.1+
	// renderers. It intentionally excludes ReleaseInfo identity and revision.
	ReleaseComponents map[string]string

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
	return newReconcile(cluster, acceleratorManager, s, metricsRemoteWriteURL, nil)
}

func NewReconcileWithReleaseInfo(cluster *v1.Cluster, acceleratorManager accelerator.Manager,
	s storage.Storage, metricsRemoteWriteURL string, resolver ReleaseComponentResolver) (ClusterReconcile, error) {
	components, err := resolveReleaseComponents(cluster, s, resolver)
	if err != nil {
		return nil, err
	}

	return newReconcile(cluster, acceleratorManager, s, metricsRemoteWriteURL, components)
}

func newReconcile(cluster *v1.Cluster, acceleratorManager accelerator.Manager,
	s storage.Storage, metricsRemoteWriteURL string, components map[string]string) (ClusterReconcile, error) {
	switch cluster.Spec.Type {
	case v1.SSHClusterType:
		legacy := &sshRayClusterReconciler{
			executor:           &command.OSExecutor{},
			acceleratorManager: acceleratorManager,
			storage:            s,
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
				releaseComponents:  components,
			}, nil
		}

		return legacy, nil
	case v1.KubernetesClusterType:
		return NewNativeKubernetesClusterReconcilerWithReleaseComponents(s, acceleratorManager, metricsRemoteWriteURL, components), nil
	default:
		return nil, fmt.Errorf("unsupported cluster type: %s", cluster.Spec.Type)
	}
}

func resolveReleaseComponents(cluster *v1.Cluster, s storage.Storage, resolver ReleaseComponentResolver) (map[string]string, error) {
	if cluster == nil || cluster.Spec == nil {
		return nil, fmt.Errorf("cluster spec is required")
	}

	releaseAware, err := isReleaseInfoAwareClusterVersion(cluster.Spec.Version)
	if err != nil {
		return nil, err
	}

	if !releaseAware {
		return nil, nil
	}

	if resolver == nil {
		return nil, fmt.Errorf("release component resolver is required for cluster version %s", cluster.Spec.Version)
	}

	if needsVersionUpgrade(cluster) {
		return resolveUpgradeComponents(cluster, s, resolver)
	}

	acceleratorType := ""
	if cluster.Spec.Config != nil && cluster.Spec.Config.AcceleratorType != nil {
		acceleratorType = *cluster.Spec.Config.AcceleratorType
	}

	components, err := resolver.ComponentsFor(cluster.Spec.Version, acceleratorType)
	if err != nil {
		return nil, fmt.Errorf("resolve release components: %w", err)
	}

	return copyReleaseComponents(components), nil
}

func resolveUpgradeComponents(cluster *v1.Cluster, s storage.Storage, resolver ReleaseComponentResolver) (map[string]string, error) {
	if s == nil {
		return nil, fmt.Errorf("storage is required to resolve upgrade components")
	}

	clusterID := strconv.Itoa(cluster.ID)

	snapshot, err := s.GetClusterUpgradeSnapshot(clusterID)
	if err == nil {
		if snapshot.TargetClusterVersion == cluster.Spec.Version &&
			snapshot.SourceClusterVersion == cluster.Status.Version {
			return copyReleaseComponents(snapshot.Components), nil
		}

		// A successful status write can be followed by a transient snapshot-delete
		// failure. Once its target is the observed version, it is no longer an
		// in-flight operation and may be replaced by the next upgrade.
		if snapshot.TargetClusterVersion == cluster.Status.Version {
			if err = s.DeleteClusterUpgradeSnapshot(clusterID); err != nil {
				return nil, fmt.Errorf("delete completed upgrade snapshot: %w", err)
			}
		} else {
			return nil, fmt.Errorf("in-flight upgrade snapshot %s -> %s does not match requested upgrade %s -> %s",
				snapshot.SourceClusterVersion, snapshot.TargetClusterVersion,
				cluster.Status.Version, cluster.Spec.Version)
		}
	} else if !errors.Is(err, storage.ErrResourceNotFound) {
		return nil, fmt.Errorf("get cluster upgrade snapshot: %w", err)
	}

	provider, ok := resolver.(ReleaseInfoProvider)
	if !ok {
		return nil, fmt.Errorf("release info provider is required to persist upgrade snapshot")
	}

	info, err := provider.Current()
	if err != nil {
		return nil, fmt.Errorf("get release info for upgrade snapshot: %w", err)
	}

	components, err := releaseInfoComponents(info, cluster.Spec.Version, clusterAcceleratorType(cluster))
	if err != nil {
		return nil, err
	}

	snapshot, err = newClusterUpgradeSnapshot(cluster, info, components)
	if err != nil {
		return nil, err
	}

	if err = s.CreateClusterUpgradeSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("create cluster upgrade snapshot: %w", err)
	}

	return copyReleaseComponents(snapshot.Components), nil
}

func releaseInfoComponents(info *v1.ReleaseInfo, version, acceleratorType string) (map[string]string, error) {
	releaseVersion := releaseInfoClusterVersion(info, version)
	if releaseVersion == nil {
		return nil, fmt.Errorf("cluster version %s is not supported by release info %s", version, info.GetName())
	}

	components := copyReleaseComponents(releaseVersion.Components)
	for name, image := range releaseVersion.AcceleratorComponents[acceleratorType] {
		if components == nil {
			components = make(map[string]string)
		}

		components[name] = image
	}

	return components, nil
}

func clusterAcceleratorType(cluster *v1.Cluster) string {
	if cluster.Spec != nil && cluster.Spec.Config != nil && cluster.Spec.Config.AcceleratorType != nil {
		return *cluster.Spec.Config.AcceleratorType
	}

	return ""
}

func newClusterUpgradeSnapshot(cluster *v1.Cluster, info *v1.ReleaseInfo, components map[string]string) (*v1.ClusterUpgradeSnapshot, error) {
	if info == nil || info.Metadata == nil || info.Status == nil {
		return nil, fmt.Errorf("release info metadata and status are required for upgrade snapshot")
	}

	target := releaseInfoClusterVersion(info, cluster.Spec.Version)
	if target == nil || target.State != v1.ReleaseInfoClusterVersionStateActive {
		return nil, fmt.Errorf("cluster version %s is not active in release info %s", cluster.Spec.Version, info.Metadata.Name)
	}

	targetReference := &v1.ReleaseInfoReference{
		Baseline: info.Metadata.Name,
		Revision: info.Status.Revision,
	}
	snapshot := &v1.ClusterUpgradeSnapshot{
		ClusterID:            cluster.ID,
		SourceClusterVersion: cluster.Status.Version,
		TargetClusterVersion: cluster.Spec.Version,
		TargetReleaseInfo:    targetReference,
		AllowedEdge: v1.ClusterUpgradeEdge{
			From: cluster.Status.Version,
			To:   cluster.Spec.Version,
		},
		Components: copyReleaseComponents(components),
	}

	source := releaseInfoClusterVersion(info, cluster.Status.Version)
	if source == nil {
		releaseAware, err := isReleaseInfoAwareClusterVersion(cluster.Status.Version)
		if err != nil {
			return nil, err
		}

		if releaseAware {
			return nil, fmt.Errorf("source cluster version %s is not supported by release info %s", cluster.Status.Version, info.Metadata.Name)
		}

		return snapshot, nil
	}

	if !containsVersion(source.UpgradeTo, cluster.Spec.Version) {
		return nil, fmt.Errorf("release info %s does not allow upgrade %s -> %s", info.Metadata.Name, cluster.Status.Version, cluster.Spec.Version)
	}

	snapshot.SourceReleaseInfo = &v1.ReleaseInfoReference{
		Baseline: info.Metadata.Name,
		Revision: info.Status.Revision,
	}

	return snapshot, nil
}

func releaseInfoClusterVersion(info *v1.ReleaseInfo, version string) *v1.ReleaseInfoClusterVersion {
	if info == nil || info.Spec == nil {
		return nil
	}

	for index := range info.Spec.ClusterVersions {
		if info.Spec.ClusterVersions[index].Version == version {
			return &info.Spec.ClusterVersions[index]
		}
	}

	return nil
}

func containsVersion(versions []string, want string) bool {
	for _, version := range versions {
		if version == want {
			return true
		}
	}

	return false
}

func isReleaseInfoAwareClusterVersion(version string) (bool, error) {
	legacy, err := semver.LessThan(version, "v1.1.0")
	if err != nil {
		return false, fmt.Errorf("invalid cluster version %q: %w", version, err)
	}

	return !legacy, nil
}

// IsReleaseInfoAwareClusterVersion reports whether a cluster version must use
// the database-backed ReleaseInfo matrix.
func IsReleaseInfoAwareClusterVersion(version string) (bool, error) {
	return isReleaseInfoAwareClusterVersion(version)
}

func copyReleaseComponents(components map[string]string) map[string]string {
	if len(components) == 0 {
		return nil
	}

	copied := make(map[string]string, len(components))
	for name, image := range components {
		copied[name] = image
	}

	return copied
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
