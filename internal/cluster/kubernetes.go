package cluster

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/component"
	"github.com/neutree-ai/neutree/internal/cluster/component/metrics"
	"github.com/neutree-ai/neutree/internal/cluster/component/router"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ ClusterManager = &NativeKubernetesCluster{}

var _ ClusterReconcile = &NativeKubernetesCluster{}

type NativeKubernetesCluster struct {
	cluster       *v1.Cluster
	imageRegistry *v1.ImageRegistry
	storage       storage.Storage

	metricsRemoteWriteURL string
}

func NewNativeKubernetesCluster(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry, metricsRemoteWriteURL string, storage storage.Storage) (*NativeKubernetesCluster, error) {
	c := &NativeKubernetesCluster{
		metricsRemoteWriteURL: metricsRemoteWriteURL,
		storage:               storage,
		cluster:               cluster,
		imageRegistry:         imageRegistry}

	return c, nil
}

func (c *NativeKubernetesCluster) Reconcile(ctx context.Context, cluster *v1.Cluster) (err error) {
	err = c.reconcile(ctx, cluster)
	if err != nil {
		cluster.Status.Phase = v1.ClusterPhaseFailed
		cluster.Status.ErrorMessage = fmt.Sprintf("Cluster reconcile failed: %v", err)
		updateErr := c.storage.UpdateCluster(cluster.GetID(), &v1.Cluster{Status: cluster.Status})
		if updateErr != nil {
			klog.Error("Failed to update cluster status: ", updateErr)
		}
		return err
	}

	cluster.Status.Phase = v1.ClusterPhaseRunning
	cluster.Status.ErrorMessage = ""
	updateErr := c.storage.UpdateCluster(cluster.GetID(), &v1.Cluster{Status: cluster.Status})
	if updateErr != nil {
		klog.Error("Failed to update cluster status: ", updateErr)
	}

	return nil
}

func (c *NativeKubernetesCluster) reconcile(ctx context.Context, cluster *v1.Cluster) error {
	config, err := util.ParseKubernetesClusterConfig(cluster) // Ensure config is parsed
	if err != nil {
		return errors.Wrap(err, "failed to parse cluster config")
	}

	ctrlClient, err := util.GetClientFromCluster(cluster) // Ensure we can create client
	if err != nil {
		return errors.Wrap(err, "failed to create Kubernetes client")
	}

	imageRegistry, err := getUsedImageRegistries(cluster, c.storage)
	if err != nil {
		return errors.Wrap(err, "failed to get used image registries")
	}

	ns := generateInstallNs(cluster)
	imagePullSecret, err := generateImagePullSecret(ns.Name, imageRegistry)
	if err != nil {
		return errors.Wrap(err, "failed to generate image pull secret")
	}

	installObjs := []client.Object{ns, imagePullSecret}
	for _, obj := range installObjs {
		err = ctrlClient.Patch(ctx, obj, client.Apply, client.ForceOwnership, client.FieldOwner("neutree"))
		if err != nil {
			return errors.Wrap(err, "failed to apply object "+obj.GetName())
		}
	}

	imagePrefix, err := getImagePrefix(imageRegistry)
	if err != nil {
		return errors.Wrap(err, "failed to get image prefix")
	}

	// Create components
	metricsComp := metrics.NewMetricsComponent(cluster.Metadata.Name, cluster.Metadata.Workspace, ns.Name, imagePrefix, imagePullSecret.Name, c.metricsRemoteWriteURL, *config, ctrlClient)
	routerComp := router.NewRouterComponent(cluster.Metadata.Name, cluster.Metadata.Workspace, ns.Name, imagePrefix, imagePullSecret.Name, *config, ctrlClient)

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

	// Get the router service endpoint
	endpoint, err := routerComp.GetRouteEndpoint(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get router service endpoint")
	}

	cluster.Status.DashboardURL = fmt.Sprintf("http://%s", endpoint)

	return nil
}

func (c *NativeKubernetesCluster) ReconcileDelete(ctx context.Context, cluster *v1.Cluster) error {
	// For native Kubernetes cluster, we don't manage the cluster lifecycle
	// The cluster should be managed externally

	return c.reconcileDelete(ctx, cluster)
}

func (c *NativeKubernetesCluster) ConnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint) error {
	// For native Kubernetes cluster, model connection is handled externally
	// No implementation needed as the cluster is pre-configured
	return nil
}

func (c *NativeKubernetesCluster) DisconnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint) error {
	// For native Kubernetes cluster, model disconnection is handled externally
	// No implementation needed as the cluster is pre-configured
	return nil
}

func (c *NativeKubernetesCluster) UpCluster(ctx context.Context, restart bool) (string, error) {
	// For native Kubernetes cluster, the cluster is already running
	// Just verify it's accessible and return success
	// The actual access information is stored in cluster status
	config, err := util.ParseKubernetesClusterConfig(c.cluster) // Ensure config is parsed
	if err != nil {
		return "", errors.Wrap(err, "failed to parse cluster config")
	}

	ctrlClient, err := util.GetClientFromCluster(c.cluster) // Ensure we can create client
	if err != nil {
		return "", errors.Wrap(err, "failed to create Kubernetes client")
	}

	ns := generateInstallNs(c.cluster)
	imagePullSecret, err := generateImagePullSecret(ns.Name, c.imageRegistry)
	if err != nil {
		return "", errors.Wrap(err, "failed to generate image pull secret")
	}

	installObjs := []client.Object{ns, imagePullSecret}
	for _, obj := range installObjs {
		err = ctrlClient.Patch(ctx, obj, client.Apply, client.ForceOwnership, client.FieldOwner("neutree"))
		if err != nil {
			return "", errors.Wrap(err, "failed to apply object "+obj.GetName())
		}
	}

	imagePrefix, err := getImagePrefix(c.imageRegistry)
	if err != nil {
		return "", errors.Wrap(err, "failed to get image prefix")
	}

	// Create components
	metricsComp := metrics.NewMetricsComponent(c.cluster.Metadata.Name, c.cluster.Metadata.Workspace, ns.Name, imagePrefix, imagePullSecret.Name, c.metricsRemoteWriteURL, *config, ctrlClient)
	routerComp := router.NewRouterComponent(c.cluster.Metadata.Name, c.cluster.Metadata.Workspace, ns.Name, imagePrefix, imagePullSecret.Name, *config, ctrlClient)

	comps := []component.Component{metricsComp, routerComp}

	var errs []error
	for _, comp := range comps {
		reconcileErr := comp.Reconcile()
		if reconcileErr != nil {
			errs = append(errs, reconcileErr)
		}
	}

	if len(errs) > 0 {
		return "", utilerrors.NewAggregate(errs)
	}

	// Get the router service endpoint
	endpoint, err := routerComp.GetRouteEndpoint(ctx)
	if err != nil {
		return "", errors.Wrap(err, "failed to get router service endpoint")
	}

	return endpoint, nil
}

func (c *NativeKubernetesCluster) DownCluster(ctx context.Context) error {
	// For native Kubernetes cluster, we don't manage the cluster lifecycle
	// The cluster should be managed externally

	ctrlClient, err := util.GetClientFromCluster(c.cluster) // Ensure we can create client
	if err != nil {
		return errors.Wrap(err, "failed to create Kubernetes client")
	}

	// Note: We only delete the namespace here. Other resources should be cleaned up by Kubernetes garbage collection.
	// If more thorough cleanup is needed, additional logic can be added here.

	ns := generateInstallNs(c.cluster)
	err = ctrlClient.Delete(ctx, ns)
	if err != nil && !apierrors.IsNotFound(err) {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "failed to delete namespace")
	}

	return errors.New("waiting for namespace deletion")
}

func (c *NativeKubernetesCluster) reconcileDelete(ctx context.Context, cluster *v1.Cluster) error {
	// For native Kubernetes cluster, we don't manage the cluster lifecycle
	// The cluster should be managed externally

	ctrlClient, err := util.GetClientFromCluster(cluster)
	if err != nil {
		klog.Infof("Kubernetes client create error, may cluster never init, so skip delete: %v", err)
		return nil
	}

	ns := generateInstallNs(cluster)
	err = ctrlClient.Delete(ctx, ns)
	if err != nil && !apierrors.IsNotFound(err) {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "failed to delete namespace")
	}

	return errors.New("waiting for namespace deletion")
}

func (c *NativeKubernetesCluster) StartNode(ctx context.Context, nodeIP string) error {
	// For native Kubernetes cluster, node lifecycle is managed externally
	return errors.New("node management is not supported for native Kubernetes cluster")
}

func (c *NativeKubernetesCluster) StopNode(ctx context.Context, nodeIP string) error {
	// For native Kubernetes cluster, node lifecycle is managed externally
	return errors.New("node management is not supported for native Kubernetes cluster")
}

func (c *NativeKubernetesCluster) GetDesireStaticWorkersIP(ctx context.Context) []string {
	// Native Kubernetes cluster doesn't expose static worker IPs
	// The workers are managed by Kubernetes and accessed through services
	return []string{}
}

func (c *NativeKubernetesCluster) Sync(ctx context.Context) error {
	// For native Kubernetes cluster, sync is minimal
	// Just verify the dashboard service is accessible

	return nil
}
