package cluster

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	acceleratoroperator "github.com/neutree-ai/neutree/internal/accelerator/operator"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/internal/cluster/component"
	"github.com/neutree-ai/neutree/internal/cluster/component/hami"
	"github.com/neutree-ai/neutree/internal/cluster/component/metrics"
	"github.com/neutree-ai/neutree/internal/cluster/component/router"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	"github.com/neutree-ai/neutree/internal/util"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
	"github.com/neutree-ai/neutree/pkg/clustercache"
	"github.com/neutree-ai/neutree/pkg/metricsconfig"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var _ ClusterReconcile = &NativeKubernetesClusterReconciler{}

type NativeKubernetesClusterReconciler struct {
	cacheProviders        *clustercache.Providers
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
		Cluster:        cluster,
		Ctx:            ctx,
		metricsTargets: metricsconfig.NewRegistry(),

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

	// Cache health is reported in its own status; ordinary inference must not
	// be blocked by an optional Runtime or its control-plane installation.
	c.reconcileCache(reconcileCtx)

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

	// Legacy and Operator paths share one accelerator owner. Operator resources
	// are selected before component rendering so Legacy can skip owned data-plane objects.
	operatorAccelerators, operatorErr := c.reconcileAcceleratorOperators(reconcileCtx, imagePrefix)
	if operatorErr != nil && operatorAccelerators == nil {
		return errors.Wrap(operatorErr, "failed to reconcile accelerator operators")
	}

	needReconcileAdditionalComps, needDeleteAdditionalComps := c.ComputeAdditionalComponents(reconcileCtx, imagePrefix)
	reconcileComps = append(reconcileComps, needReconcileAdditionalComps...)
	reconcileDeleteComps = append(reconcileDeleteComps, needDeleteAdditionalComps...)

	var errs []error
	if operatorErr != nil {
		errs = append(errs, operatorErr)
	}

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

// reconcileAcceleratorOperators dispatches accelerator types that have a
// registered Kubernetes Operator factory. Legacy accelerator types remain
// owned by Metrics/HAMi profile and configuration injection.
func (c *NativeKubernetesClusterReconciler) reconcileAcceleratorOperators(
	reconcileCtx *ReconcileContext,
	imagePrefix string,
) (map[string]struct{}, error) {
	operatorAccelerators := map[string]struct{}{}
	reconcileCtx.acceleratorOperators = map[string]acceleratoroperator.AcceleratorOperator{}
	var readinessErrors []error

	if c.acceleratorMgr == nil {
		return operatorAccelerators, nil
	}

	// The current Kubernetes virtualization contract permits one matching owner.
	// Build the complete operator set before reconciling so conflicts fail atomically.
	for _, acceleratorType := range c.acceleratorMgr.SupportPlugins() {
		acceleratorPlugin, ok := c.acceleratorMgr.GetPlugin(acceleratorType)
		if !ok || acceleratorPlugin == nil {
			continue
		}

		if !publicaccelerator.PluginMatchesCluster(acceleratorPlugin, reconcileCtx.Cluster) {
			continue
		}

		if c.acceleratorMgr.PluginDependencyDeployMode(acceleratorType, string(v1.KubernetesClusterType)) != accelerator.DependencyDeployModeOperator {
			continue
		}

		acceleratorOperator, err := c.acceleratorMgr.OperatorForKubernetesCluster(acceleratorType, reconcileCtx.Cluster)
		if err != nil {
			return nil, errors.Wrapf(err, "create accelerator operator for %s", acceleratorType)
		}

		reconcileCtx.acceleratorOperators[acceleratorType] = acceleratorOperator
	}

	if len(reconcileCtx.acceleratorOperators) > 1 {
		types := make([]string, 0, len(reconcileCtx.acceleratorOperators))
		for acceleratorType := range reconcileCtx.acceleratorOperators {
			types = append(types, acceleratorType)
		}
		sort.Strings(types)

		return nil, errors.Errorf("multiple accelerator operators match cluster: %s", strings.Join(types, ", "))
	}

	for acceleratorType, acceleratorOperator := range reconcileCtx.acceleratorOperators {

		operatorConfig := v1.KubernetesClusterConfig{}
		if reconcileCtx.kubernetesClusterConfig != nil {
			operatorConfig = *reconcileCtx.kubernetesClusterConfig
		}

		operatorContext := &acceleratoroperator.OperatorContext{
			Cluster:         reconcileCtx.Cluster,
			Client:          reconcileCtx.ctrClient,
			Namespace:       reconcileCtx.clusterNamespace,
			ImagePrefix:     imagePrefix,
			ImagePullSecret: ImagePullSecretName,
			Config:          operatorConfig,
			MetricsTargets:  reconcileCtx.metricsTargets,
		}

		if _, err := acceleratorOperator.Reconcile(reconcileCtx.Ctx, operatorContext); err != nil {
			return nil, errors.Wrapf(err, "reconcile accelerator operator %s", acceleratorType)
		}

		status, err := acceleratorOperator.Status(reconcileCtx.Ctx, operatorContext)
		if err != nil {
			return nil, errors.Wrapf(err, "get accelerator operator %s status", acceleratorType)
		}

		if status != nil {
			if reconcileCtx.Cluster.Status == nil {
				reconcileCtx.Cluster.Status = &v1.ClusterStatus{}
			}

			if reconcileCtx.Cluster.Status.ComponentStatus == nil {
				reconcileCtx.Cluster.Status.ComponentStatus = map[string]*v1.ComponentStatus{}
			}

			reconcileCtx.Cluster.Status.ComponentStatus[fmt.Sprintf("accelerator-operator/%s", acceleratorOperator.Name())] = status

			if status.Phase != v1.ComponentPhaseReady {
				readinessErrors = append(readinessErrors, errors.Errorf("accelerator operator %s is not ready: %s: %s", acceleratorType, status.Reason, status.Message))
			}
		} else {
			readinessErrors = append(readinessErrors, errors.Errorf("accelerator operator %s has no status", acceleratorType))
		}

		operatorAccelerators[acceleratorType] = struct{}{}
	}

	return operatorAccelerators, utilerrors.NewAggregate(readinessErrors)
}

// inferenceScrapeRules derives the metrics-scrape adjustments for this cluster
// from the engines registered in its workspace.
//
// A listing failure is not fatal: it yields the zero value, which renders the
// inference job exactly as it did before engines could declare capabilities.
// Failing the whole cluster reconcile over a capability refinement would be a
// worse outcome than scraping one engine that asked not to be scraped.
func (c *NativeKubernetesClusterReconciler) inferenceScrapeRules(cluster *v1.Cluster) metrics.InferenceScrapeRules {
	if c.storage == nil {
		return metrics.InferenceScrapeRules{}
	}

	engines, err := c.storage.ListEngine(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(cluster.Metadata.Workspace),
			},
		},
	})
	if err != nil {
		klog.Warningf("failed to list engines for cluster %s, falling back to scraping every inference pod: %v",
			cluster.Metadata.WorkspaceName(), err)

		return metrics.InferenceScrapeRules{}
	}

	return metrics.BuildInferenceScrapeRules(engines, v1.DefaultMetricsExportPort, v1.DefaultMetricsExportPath)
}

func (c *NativeKubernetesClusterReconciler) ComputeAdditionalComponents(reconcileCtx *ReconcileContext,
	imagePrefix string) ([]component.Component, []component.Component) {
	reconcileComps := []component.Component{}
	reconcileDeleteComps := []component.Component{}

	operatorTypes := map[string]struct{}{}
	for acceleratorType := range reconcileCtx.acceleratorOperators {
		operatorTypes[acceleratorType] = struct{}{}
	}

	// Metrics remains the Legacy owner only for accelerator types absent from
	// reconcileCtx.acceleratorOperators.
	metricsComp := metrics.NewMetricsComponent(reconcileCtx.Cluster,
		reconcileCtx.clusterNamespace, imagePrefix, ImagePullSecretName,
		c.metricsRemoteWriteURL, *reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient, c.acceleratorMgr).
		WithExcludedAccelerators(operatorTypes).
		WithAcceleratorOperators(reconcileCtx.acceleratorOperators).
		WithMetricsTargets(reconcileCtx.metricsTargets).
		WithInferenceScrapeRules(c.inferenceScrapeRules(reconcileCtx.Cluster)).
		WithCacheProviders(c.cacheProviders)
	reconcileComps = append(reconcileComps, metricsComp)

	// Hami always owns the control plane. An Operator-owned accelerator hands
	// only its device plugin and node scope to the Operator.
	hamiComp := hami.NewHAMiComponent(reconcileCtx.Cluster,
		reconcileCtx.clusterNamespace, imagePrefix, ImagePullSecretName,
		*reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient, c.acceleratorMgr).
		WithExcludedAccelerators(operatorTypes)

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
		Cluster:        cluster,
		Ctx:            ctx,
		metricsTargets: metricsconfig.NewRegistry(),

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
			_, deleteErr := c.deleteAcceleratorOperators(reconcileCtx)
			return deleteErr
		}

		return errors.Wrap(err, "failed to get namespace")
	}

	if ns.DeletionTimestamp != nil {
		return errors.New("waiting for namespace deletion")
	}

	if err := c.deleteCache(reconcileCtx); err != nil {
		return err
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

func (c *NativeKubernetesClusterReconciler) deleteAcceleratorOperators(reconcileCtx *ReconcileContext) (map[string]struct{}, error) {
	owned := map[string]struct{}{}
	reconcileCtx.acceleratorOperators = map[string]acceleratoroperator.AcceleratorOperator{}

	if c.acceleratorMgr == nil {
		return owned, nil
	}
	var errs []error

	for _, acceleratorType := range c.acceleratorMgr.SupportPlugins() {
		acceleratorPlugin, ok := c.acceleratorMgr.GetPlugin(acceleratorType)
		if !ok || acceleratorPlugin == nil || !publicaccelerator.PluginMatchesCluster(acceleratorPlugin, reconcileCtx.Cluster) {
			continue
		}

		if c.acceleratorMgr.PluginDependencyDeployMode(acceleratorType, string(v1.KubernetesClusterType)) != accelerator.DependencyDeployModeOperator {
			continue
		}

		owned[acceleratorType] = struct{}{}

		instance, err := c.acceleratorMgr.OperatorForKubernetesCluster(acceleratorType, reconcileCtx.Cluster)
		if err != nil {
			return nil, errors.Wrapf(err, "create accelerator operator %s for deletion", acceleratorType)
		}

		reconcileCtx.acceleratorOperators[acceleratorType] = instance

		opCtx := &acceleratoroperator.OperatorContext{
			Cluster: reconcileCtx.Cluster, Client: reconcileCtx.ctrClient, Namespace: reconcileCtx.clusterNamespace,
			MetricsTargets: reconcileCtx.metricsTargets,
		}
		if reconcileCtx.kubernetesClusterConfig != nil {
			opCtx.Config = *reconcileCtx.kubernetesClusterConfig
		}

		if err := instance.Delete(reconcileCtx.Ctx, opCtx); err != nil {
			errs = append(errs, errors.Wrapf(err, "delete accelerator operator %s", acceleratorType))
		}
	}

	return owned, utilerrors.NewAggregate(errs)
}

func (c *NativeKubernetesClusterReconciler) deleteClusterComponents(
	reconcileCtx *ReconcileContext,
) error {
	operatorTypes, err := c.deleteAcceleratorOperators(reconcileCtx)
	if err != nil {
		return err
	}
	var errs []error

	if shouldDeleteAcceleratorVirtualizationComponent(reconcileCtx.Cluster) {
		hamiComp := hami.NewHAMiComponent(reconcileCtx.Cluster,
			reconcileCtx.clusterNamespace, "", ImagePullSecretName,
			*reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient, c.acceleratorMgr).WithExcludedAccelerators(operatorTypes)
		if err := hamiComp.Delete(); err != nil {
			errs = append(errs, errors.Wrap(err, "failed to delete accelerator virtualization component"))
		}
	}

	metricsComp := metrics.NewMetricsComponent(reconcileCtx.Cluster,
		reconcileCtx.clusterNamespace, "", ImagePullSecretName,
		c.metricsRemoteWriteURL, *reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient, c.acceleratorMgr).
		WithExcludedAccelerators(operatorTypes).
		WithAcceleratorOperators(reconcileCtx.acceleratorOperators)
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
