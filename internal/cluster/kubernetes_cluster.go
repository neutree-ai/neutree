package cluster

import (
	"context"
	"strconv"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/component"
	"github.com/neutree-ai/neutree/internal/cluster/component/metrics"
	"github.com/neutree-ai/neutree/internal/cluster/component/router"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var _ ClusterReconcile = &NativeKubernetesClusterReconciler{}

type NativeKubernetesClusterReconciler struct {
	storage storage.Storage

	metricsRemoteWriteURL string
}

func NewNativeKubernetesClusterReconciler(
	storage storage.Storage, metricsRemoteWriteURL string) *NativeKubernetesClusterReconciler {
	c := &NativeKubernetesClusterReconciler{
		metricsRemoteWriteURL: metricsRemoteWriteURL,
		storage:               storage,
	}

	return c
}

func (c *NativeKubernetesClusterReconciler) Reconcile(ctx context.Context, cluster *v1.Cluster) (err error) {
	reconcileCtx := &ReconcileContext{
		Cluster: cluster,
		Ctx:     ctx,

		clusterNamespace: util.ClusterNamespace(cluster),
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
	if reconcileCtx.Cluster.Status == nil {
		reconcileCtx.Cluster.Status = &v1.ClusterStatus{}
	}

	if !reconcileCtx.Cluster.Status.Initialized {
		reconcileCtx.Cluster.Status.Phase = v1.ClusterPhaseInitializing

		err := c.storage.UpdateCluster(strconv.Itoa(reconcileCtx.Cluster.ID), reconcileCtx.Cluster)
		if err != nil {
			return errors.Wrap(err, "failed to update cluster status")
		}
	}

	ns := generateInstallNs(reconcileCtx.Cluster)

	imagePullSecret, err := generateImagePullSecret(ns.Name, reconcileCtx.ImageRegistry)
	if err != nil {
		return errors.Wrap(err, "failed to generate image pull secret")
	}

	installObjs := []client.Object{ns, imagePullSecret}
	for _, obj := range installObjs {
		err = util.CreateOrPatch(reconcileCtx.Ctx, obj, reconcileCtx.ctrClient)
		if err != nil {
			return errors.Wrap(err, "failed to create or patch object "+obj.GetName())
		}
	}

	imagePrefix, err := util.GetImagePrefix(reconcileCtx.ImageRegistry)
	if err != nil {
		return errors.Wrap(err, "failed to get image prefix")
	}

	// Create components
	metricsComp := metrics.NewMetricsComponent(reconcileCtx.Cluster,
		ns.Name, imagePrefix, imagePullSecret.Name,
		c.metricsRemoteWriteURL, *reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient)
	routerComp := router.NewRouterComponent(reconcileCtx.Cluster,
		ns.Name, imagePrefix, imagePullSecret.Name, *reconcileCtx.kubernetesClusterConfig, reconcileCtx.ctrClient)

	comps := []component.Component{metricsComp, routerComp}

	var errs []error

	for _, comp := range comps {
		err = comp.Reconcile()
		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}

	// Save cluster annotations (including last applied configs from components)
	err = c.storage.UpdateCluster(reconcileCtx.Cluster.GetID(), &v1.Cluster{
		Metadata: reconcileCtx.Cluster.Metadata,
	})
	if err != nil {
		return errors.Wrap(err, "failed to update cluster annotations")
	}

	// Get the router service endpoint
	endpoint, err := routerComp.GetRouteEndpoint(reconcileCtx.Ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get router service endpoint")
	}

	reconcileCtx.Cluster.Status.DashboardURL = endpoint

	return nil
}

func (c *NativeKubernetesClusterReconciler) ReconcileDelete(ctx context.Context, cluster *v1.Cluster) error {
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
		// Namespace is already being deleted
		return errors.New("waiting for namespace deletion")
	}

	err = reconcileCtx.ctrClient.Delete(reconcileCtx.Ctx, ns)
	if err != nil {
		return errors.Wrap(err, "failed to delete namespace")
	}

	return errors.New("waiting for namespace deletion")
}
