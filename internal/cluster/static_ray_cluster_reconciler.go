package cluster

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type staticRayClusterStore interface {
	ListImageRegistry(option storage.ListOption) ([]v1.ImageRegistry, error)
	ListStaticNodeCluster(option storage.ListOption) ([]v1.StaticNodeCluster, error)
	CreateStaticNodeCluster(data *v1.StaticNodeCluster) error
	UpdateStaticNodeCluster(id string, data *v1.StaticNodeCluster) error
}

type staticRayClusterReconciler struct {
	store                 staticRayClusterStore
	acceleratorManager    accelerator.Manager
	metricsRemoteWriteURL string
	newDashboardService   dashboard.NewDashboardServiceFunc
}

var _ ClusterReconcile = (*staticRayClusterReconciler)(nil)

func newStaticRayClusterReconcile(
	store interface{},
	acceleratorManager accelerator.Manager,
	metricsRemoteWriteURL string,
) (*staticRayClusterReconciler, error) {
	staticStore, ok := store.(staticRayClusterStore)
	if !ok {
		return nil, errors.New("storage does not support static node cluster resources")
	}

	return &staticRayClusterReconciler{
		store:                 staticStore,
		acceleratorManager:    acceleratorManager,
		metricsRemoteWriteURL: metricsRemoteWriteURL,
		newDashboardService:   dashboard.NewDashboardService,
	}, nil
}

func (r *staticRayClusterReconciler) Reconcile(ctx context.Context, cluster *v1.Cluster) error {
	if cluster == nil || cluster.Metadata == nil {
		return errors.New("cluster metadata is required")
	}

	desired, err := r.buildStaticNodeCluster(cluster)
	if err != nil {
		return err
	}

	current, found, err := r.findStaticNodeCluster(cluster.Metadata.Workspace, cluster.Metadata.Name)
	if err != nil {
		return errors.Wrap(err, "failed to find static node cluster")
	}

	if !found {
		if err := r.store.CreateStaticNodeCluster(desired); err != nil {
			return errors.Wrap(err, "failed to create static node cluster")
		}

		r.copyStaticStatusToCluster(cluster, desired, nil)

		return errors.Errorf("static node cluster %s is provisioning", cluster.Metadata.Name)
	}

	desired.ID = current.ID
	if err := r.store.UpdateStaticNodeCluster(strconv.Itoa(current.ID), desired); err != nil {
		return errors.Wrap(err, "failed to update static node cluster")
	}

	r.copyStaticStatusToCluster(cluster, desired, current.Status)

	if current.Status == nil || current.Status.Phase != v1.StaticNodeClusterPhaseReady {
		return staticNodeClusterNotReadyError(current)
	}

	return nil
}

func (r *staticRayClusterReconciler) ReconcileDelete(_ context.Context, cluster *v1.Cluster) error {
	if cluster == nil || cluster.Metadata == nil {
		return errors.New("cluster metadata is required")
	}

	current, found, err := r.findStaticNodeCluster(cluster.Metadata.Workspace, cluster.Metadata.Name)
	if err != nil {
		return errors.Wrap(err, "failed to find static node cluster")
	}

	if !found {
		return nil
	}

	if current.Metadata == nil {
		current.Metadata = &v1.Metadata{}
	}

	if current.Metadata.DeletionTimestamp == "" {
		current.Metadata.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)
	}

	return r.store.UpdateStaticNodeCluster(strconv.Itoa(current.ID), current)
}

func (r *staticRayClusterReconciler) buildStaticNodeCluster(cluster *v1.Cluster) (*v1.StaticNodeCluster, error) {
	if cluster.Spec == nil {
		return nil, errors.New("cluster spec is required")
	}

	sshConfig, err := util.ParseSSHClusterConfig(cluster)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse ssh cluster config")
	}

	if sshConfig.Provider.HeadIP == "" {
		return nil, errors.New("head IP can not be empty")
	}

	imageRegistry, err := getUsedImageRegistryFromStore(cluster, r.store)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get used image registry")
	}

	imagePrefix, err := util.GetImagePrefix(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build image prefix")
	}

	headName := sshConfig.Provider.HeadIP
	nodes := []v1.StaticNodeClusterNodeSpec{
		{
			Name:    headName,
			IP:      sshConfig.Provider.HeadIP,
			Role:    v1.StaticNodeRoleHead,
			SSHAuth: copyAuth(&sshConfig.Auth),
		},
	}

	for _, workerIP := range sshConfig.Provider.WorkerIPs {
		if workerIP == "" {
			continue
		}

		nodes = append(nodes, v1.StaticNodeClusterNodeSpec{
			Name:    workerIP,
			IP:      workerIP,
			Role:    v1.StaticNodeRoleWorker,
			SSHAuth: copyAuth(&sshConfig.Auth),
		})
	}

	return &v1.StaticNodeCluster{
		APIVersion: "v1",
		Kind:       "StaticNodeCluster",
		Metadata: &v1.Metadata{
			Name:        cluster.Metadata.Name,
			Workspace:   cluster.Metadata.Workspace,
			Labels:      copyStringMap(cluster.Metadata.Labels),
			Annotations: copyStringMap(cluster.Metadata.Annotations),
		},
		Spec: &v1.StaticNodeClusterSpec{
			Version:               cluster.Spec.Version,
			ImageRegistry:         imagePrefix,
			MetricsRemoteWriteURL: r.metricsRemoteWriteURL,
			Head:                  v1.StaticNodeClusterHeadSpec{NodeName: headName},
			Nodes:                 nodes,
		},
	}, nil
}

func (r *staticRayClusterReconciler) findStaticNodeCluster(
	workspace string,
	name string,
) (*v1.StaticNodeCluster, bool, error) {
	filters := []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, name)},
	}
	if workspace != "" {
		filters = append(filters, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, workspace),
		})
	}

	clusters, err := r.store.ListStaticNodeCluster(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, false, err
	}

	if len(clusters) == 0 {
		return nil, false, nil
	}

	return &clusters[0], true, nil
}

func (r *staticRayClusterReconciler) copyStaticStatusToCluster(
	cluster *v1.Cluster,
	desired *v1.StaticNodeCluster,
	status *v1.StaticNodeClusterStatus,
) {
	if cluster.Status == nil {
		cluster.Status = &v1.ClusterStatus{}
	}

	cluster.Status.DashboardURL = staticNodeClusterDashboardURL(desired)
	cluster.Status.DesiredNodes = len(desired.Spec.Nodes)

	if status != nil {
		cluster.Status.ReadyNodes = status.ReadyNodes
		if status.DesiredNodes > 0 {
			cluster.Status.DesiredNodes = status.DesiredNodes
		}
	}

	if status != nil && status.Phase == v1.StaticNodeClusterPhaseReady {
		cluster.Status.Initialized = true
		cluster.Status.Version = cluster.GetVersion()

		if resources, err := r.calculateStaticClusterResources(desired); err != nil {
			klog.Warningf("failed to calculate static cluster resources for %s: %v", cluster.Metadata.WorkspaceName(), err)
		} else {
			cluster.Status.ResourceInfo = resources
		}
	}
}

func (r *staticRayClusterReconciler) calculateStaticClusterResources(
	cluster *v1.StaticNodeCluster,
) (*v1.ClusterResources, error) {
	return calculateRayDashboardClusterResources(
		r.dashboardService(staticNodeClusterDashboardURL(cluster)),
		r.resourceParsers(),
	)
}

func (r *staticRayClusterReconciler) dashboardService(dashboardURL string) dashboard.DashboardService {
	if r != nil && r.newDashboardService != nil {
		return r.newDashboardService(dashboardURL)
	}

	return dashboard.NewDashboardService(dashboardURL)
}

func (r *staticRayClusterReconciler) resourceParsers() map[string]plugin.ResourceParser {
	if r == nil || r.acceleratorManager == nil {
		return nil
	}

	return r.acceleratorManager.GetAllParsers()
}

func staticNodeClusterDashboardURL(cluster *v1.StaticNodeCluster) string {
	return fmt.Sprintf("http://%s:%d", staticNodeClusterHeadIP(cluster), defaultRayDashboardPort)
}

func staticNodeClusterNotReadyError(cluster *v1.StaticNodeCluster) error {
	if cluster == nil || cluster.Status == nil {
		return errors.New("static node cluster is not ready")
	}

	if cluster.Status.ErrorMessage != "" {
		return errors.Errorf("static node cluster %s is not ready: %s", cluster.Metadata.Name, cluster.Status.ErrorMessage)
	}

	return errors.Errorf("static node cluster %s is not ready: phase=%s", cluster.Metadata.Name, cluster.Status.Phase)
}

func getUsedImageRegistryFromStore(cluster *v1.Cluster, store staticRayClusterStore) (*v1.ImageRegistry, error) {
	filters := []storage.Filter{
		{
			Column:   "metadata->name",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Spec.ImageRegistry),
		},
	}

	if cluster.Metadata.Workspace != "" {
		filters = append(filters, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Metadata.Workspace),
		})
	}

	imageRegistries, err := store.ListImageRegistry(storage.ListOption{Filters: filters})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list image registry")
	}

	if len(imageRegistries) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	imageRegistry := &imageRegistries[0]
	if imageRegistry.Status == nil || imageRegistry.Status.Phase != v1.ImageRegistryPhaseCONNECTED {
		return nil, errors.New("image registry " + cluster.Spec.ImageRegistry + " not ready")
	}

	return imageRegistry, nil
}
