package cluster

import (
	"context"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/internal/cluster/component"
	"github.com/neutree-ai/neutree/internal/cluster/component/hami"
	"github.com/neutree-ai/neutree/internal/cluster/component/metrics"
	"github.com/neutree-ai/neutree/internal/cluster/component/router"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var _ ClusterReconcile = &NativeKubernetesClusterReconciler{}

type NativeKubernetesClusterReconciler struct {
	storage               storage.Storage
	acceleratorMgr        accelerator.Manager
	metricsRemoteWriteURL string
}

func NewNativeKubernetesClusterReconciler(
	storage storage.Storage,
	acceleratorMgr accelerator.Manager,
	metricsRemoteWriteURL string) *NativeKubernetesClusterReconciler {
	c := &NativeKubernetesClusterReconciler{
		metricsRemoteWriteURL: metricsRemoteWriteURL,
		storage:               storage,
		acceleratorMgr:        acceleratorMgr,
	}

	return c
}

func (c *NativeKubernetesClusterReconciler) Reconcile(ctx context.Context, cluster *v1.Cluster) (err error) {
	WriteEarlyStatus(cluster, c.storage)

	reconcileCtx := &ReconcileContext{
		Cluster: cluster,
		Ctx:     ctx,

		clusterNamespace: util.ClusterNamespace(cluster),
		logger:           klog.LoggerWithValues(klog.Background(), "cluster", cluster.Metadata.WorkspaceName()),
	}

	err = c.generateConfig(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to generate config")
	}

	return c.reconcile(reconcileCtx)
}

func (c *NativeKubernetesClusterReconciler) generateConfig(reconcileCtx *ReconcileContext) error {
	imageRegistry, err := getUsedImageRegistries(reconcileCtx.Cluster, c.storage)
	if err != nil {
		return errors.Wrap(err, "failed to get used image registries")
	}

	reconcileCtx.ImageRegistry = imageRegistry

	config, err := util.ParseKubernetesClusterConfig(reconcileCtx.Cluster)
	if err != nil {
		return errors.Wrap(err, "failed to parse kubernetes cluster config")
	}

	reconcileCtx.kubernetesClusterConfig = config

	ctrlClient, err := util.GetClientFromCluster(reconcileCtx.Cluster)
	if err != nil {
		return errors.Wrap(err, "failed to create kubernetes client from cluster")
	}

	reconcileCtx.ctrClient = ctrlClient

	return nil
}

func (c *NativeKubernetesClusterReconciler) reconcile(reconcileCtx *ReconcileContext) error {
	ns := generateInstallNs(reconcileCtx.Cluster)

	imagePullSecret, err := generateImagePullSecret(ns.Name, reconcileCtx.ImageRegistry)
	if err != nil {
		return errors.Wrap(err, "failed to generate image pull secret")
	}

	imagePullSecret.Labels = map[string]string{
		v1.LabelManagedBy:                  v1.LabelManagedByValue,
		v1.NeutreeClusterLabelKey:          reconcileCtx.Cluster.Metadata.Name,
		v1.NeutreeClusterWorkspaceLabelKey: reconcileCtx.Cluster.Metadata.Workspace,
	}

	installObjs := []client.Object{ns, imagePullSecret}
	for _, obj := range installObjs {
		err = util.CreateOrPatch(reconcileCtx.Ctx, obj, reconcileCtx.ctrClient)
		if err != nil {
			return errors.Wrap(err, "failed to create or patch object "+obj.GetName())
		}
	}

	reconcileFuncs := []func(*ReconcileContext) error{
		c.reconcileComponents,
		c.reconcileModelCache,
	}

	var errs []error

	for _, fn := range reconcileFuncs {
		err = fn(reconcileCtx)
		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}

	// Update status fields after successful reconcile
	cluster := reconcileCtx.Cluster
	if cluster.Status == nil {
		cluster.Status = &v1.ClusterStatus{}
	}

	cluster.Status.Initialized = true

	// When reconcile succeeds, all components (Router, Metrics) have verified
	// that their Pods are running with the correct version. Set status.version
	// to match spec.version.
	if cluster.GetVersion() != "" {
		cluster.Status.Version = cluster.GetVersion()
	}

	// Calculate resources (best-effort)
	resources, err := c.calculateResources(reconcileCtx)
	if err != nil {
		klog.Warningf("failed to calculate cluster resources for %s: %v", cluster.Metadata.WorkspaceName(), err)
	} else {
		cluster.Status.ResourceInfo = resources
	}

	return nil
}

func (c *NativeKubernetesClusterReconciler) reconcileComponents(reconcileCtx *ReconcileContext) error {
	imagePrefix, err := util.GetImagePrefix(reconcileCtx.ImageRegistry)
	if err != nil {
		return errors.Wrap(err, "failed to get image prefix")
	}

	reconcileComps := []component.Component{}
	reconcileDeleteComps := []component.Component{}

	// The Router component is a core component of the cluster and cannot be removed; it should be added first.
	routerComp := router.NewRouterComponent(reconcileCtx.Cluster,
		reconcileCtx.clusterNamespace, imagePrefix, ImagePullSecretName, *reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient)
	reconcileComps = append(reconcileComps, routerComp)

	needReconcileAdditionalComps, needDeleteAdditionalComps := c.ComputeAdditionalComponents(reconcileCtx, imagePrefix)
	reconcileComps = append(reconcileComps, needReconcileAdditionalComps...)
	reconcileDeleteComps = append(reconcileDeleteComps, needDeleteAdditionalComps...)

	var errs []error

	for _, comp := range reconcileComps {
		err = comp.Reconcile()
		if err != nil {
			errs = append(errs, err)
		}
	}

	for _, comp := range reconcileDeleteComps {
		err = comp.Delete()
		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}

	// Update DashboardURL after successful reconcile
	if endpoint, routerErr := routerComp.GetRouteEndpoint(reconcileCtx.Ctx); routerErr != nil {
		klog.Warningf("failed to get route endpoint for cluster %s: %v", reconcileCtx.Cluster.Metadata.WorkspaceName(), routerErr)
	} else {
		if reconcileCtx.Cluster.Status == nil {
			reconcileCtx.Cluster.Status = &v1.ClusterStatus{}
		}

		reconcileCtx.Cluster.Status.DashboardURL = endpoint
	}

	return nil
}

func (c *NativeKubernetesClusterReconciler) ComputeAdditionalComponents(reconcileCtx *ReconcileContext,
	imagePrefix string) ([]component.Component, []component.Component) {
	reconcileComps := []component.Component{}
	reconcileDeleteComps := []component.Component{}

	metricsComp := metrics.NewMetricsComponent(reconcileCtx.Cluster,
		reconcileCtx.clusterNamespace, imagePrefix, ImagePullSecretName,
		c.metricsRemoteWriteURL, *reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient, c.acceleratorMgr)
	reconcileComps = append(reconcileComps, metricsComp)

	hamiComp := hami.NewHAMiComponent(reconcileCtx.Cluster,
		reconcileCtx.clusterNamespace, imagePrefix, ImagePullSecretName,
		*reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient, c.acceleratorMgr)
	if reconcileCtx.Cluster.Spec.AcceleratorVirtualizationEnabled() {
		reconcileComps = append(reconcileComps, hamiComp)
	} else {
		reconcileDeleteComps = append(reconcileDeleteComps, hamiComp)
	}

	return reconcileComps, reconcileDeleteComps
}

func (c *NativeKubernetesClusterReconciler) ReconcileDelete(ctx context.Context, cluster *v1.Cluster) error {
	WriteEarlyDeleting(cluster, c.storage)

	reconcileCtx := &ReconcileContext{
		Cluster: cluster,
		Ctx:     ctx,

		clusterNamespace: util.ClusterNamespace(cluster),
	}

	err := c.generateConfig(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to generate config")
	}

	return c.reconcileDelete(reconcileCtx)
}

func (c *NativeKubernetesClusterReconciler) reconcileDelete(reconcileCtx *ReconcileContext) error {
	ns := generateInstallNs(reconcileCtx.Cluster)

	err := reconcileCtx.ctrClient.Get(reconcileCtx.Ctx, client.ObjectKey{Name: ns.Name}, ns)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}

		return errors.Wrap(err, "failed to get namespace")
	}

	if ns.DeletionTimestamp != nil {
		return errors.New("waiting for namespace deletion")
	}

	if err := c.deleteClusterComponents(reconcileCtx); err != nil {
		return err
	}

	err = reconcileCtx.ctrClient.Delete(reconcileCtx.Ctx, ns)
	if err != nil {
		return errors.Wrap(err, "failed to delete namespace")
	}

	return errors.New("waiting for namespace deletion")
}

func (c *NativeKubernetesClusterReconciler) deleteClusterComponents(
	reconcileCtx *ReconcileContext,
) error {
	var errs []error

	if shouldDeleteAcceleratorVirtualizationComponent(reconcileCtx.Cluster) {
		hamiComp := hami.NewHAMiComponent(reconcileCtx.Cluster,
			reconcileCtx.clusterNamespace, "", ImagePullSecretName,
			*reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient, c.acceleratorMgr)

		if err := hamiComp.Delete(); err != nil {
			errs = append(errs, errors.Wrap(err, "failed to delete accelerator virtualization component"))
		}
	}

	metricsComp := metrics.NewMetricsComponent(reconcileCtx.Cluster,
		reconcileCtx.clusterNamespace, "", ImagePullSecretName,
		c.metricsRemoteWriteURL, *reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient, c.acceleratorMgr)
	if err := metricsComp.Delete(); err != nil {
		errs = append(errs, errors.Wrap(err, "failed to delete metrics component"))
	}

	routerComp := router.NewRouterComponent(reconcileCtx.Cluster,
		reconcileCtx.clusterNamespace, "", ImagePullSecretName,
		*reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient)
	if err := routerComp.Delete(); err != nil {
		errs = append(errs, errors.Wrap(err, "failed to delete router component"))
	}

	return utilerrors.NewAggregate(errs)
}

func shouldDeleteAcceleratorVirtualizationComponent(cluster *v1.Cluster) bool {
	if cluster == nil {
		return false
	}

	if cluster.Spec != nil && cluster.Spec.AcceleratorVirtualizationEnabled() {
		return true
	}

	if cluster.Status == nil || cluster.Status.ComponentStatus == nil {
		return false
	}

	_, ok := cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey]

	return ok
}

// calculateResources calculates the allocatable and available resources of the cluster.
func (c *NativeKubernetesClusterReconciler) calculateResources( //nolint:gocyclo
	reconcileCtx *ReconcileContext,
) (*v1.ClusterResources, error) {
	parsers := map[string]resourceparser.ResourceParser{}
	if c.acceleratorMgr != nil {
		parsers = c.acceleratorMgr.GetAllParsers()
	}

	resourceClient := resourceview.NewK8sResourceClient(reconcileCtx.ctrClient, parsers)
	resourceBuilder := resourceview.NewResourceViewBuilder(resourceClient)

	return resourceBuilder.BuildClusterResources(reconcileCtx.Ctx, reconcileCtx.Cluster)
}
