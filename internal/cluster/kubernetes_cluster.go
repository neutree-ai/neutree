package cluster

import (
	"context"
	"fmt"
	"strconv"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog"
	resourceutil "k8s.io/kubectl/pkg/util/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/cluster/component"
	"github.com/neutree-ai/neutree/internal/cluster/component/metrics"
	"github.com/neutree-ai/neutree/internal/cluster/component/router"
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
	reconcileCtx.Cluster.Status.Initialized = true

	// Collect cluster resources if cluster is running
	if reconcileCtx.Cluster.Status.Phase == v1.ClusterPhaseRunning {
		resources, err := c.calculateResources(reconcileCtx)
		if err != nil {
			return errors.Wrap(err, "failed to calculate cluster resources")
		}

		reconcileCtx.Cluster.Status.ResourceInfo = resources
	}

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

// calculateResources calculates the allocatable and available resources of the cluster.
func (c *NativeKubernetesClusterReconciler) calculateResources( //nolint:gocyclo
	reconcileCtx *ReconcileContext,
) (*v1.ClusterResources, error) {
	// todo: improve function performance through caching
	nodeList := &corev1.NodeList{}

	err := reconcileCtx.ctrClient.List(reconcileCtx.Ctx, nodeList)
	if err != nil {
		return nil, fmt.Errorf("failed to list k8s nodes: %w", err)
	}

	podList := &corev1.PodList{}

	err = reconcileCtx.ctrClient.List(reconcileCtx.Ctx, podList, &client.ListOptions{
		Raw: &metav1.ListOptions{
			FieldSelector: "spec.nodeName!=,status.phase!=Failed,status.phase!=Succeeded",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list k8s pods: %w", err)
	}

	type nodeResourceInfo struct {
		allocatableResource map[corev1.ResourceName]resource.Quantity
		availableResource   map[corev1.ResourceName]resource.Quantity
		labels              map[string]string
	}
	nodeResources := make(map[string]*nodeResourceInfo)

	for _, node := range nodeList.Items {
		if node.Spec.Unschedulable {
			continue
		}

		nodeInfo := &nodeResourceInfo{
			allocatableResource: make(map[corev1.ResourceName]resource.Quantity),
			availableResource:   make(map[corev1.ResourceName]resource.Quantity),
			labels:              node.Labels,
		}

		nodeInfo.allocatableResource = node.Status.Allocatable.DeepCopy()
		nodeInfo.availableResource = node.Status.Allocatable.DeepCopy()

		nodeResources[node.Name] = nodeInfo
	}

	for _, pod := range podList.Items {
		nodeName := pod.Spec.NodeName
		// double check
		if nodeName == "" {
			continue
		}

		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			continue
		}

		nodeInfo, exists := nodeResources[nodeName]
		if !exists {
			continue
		}

		totalRequested, _ := resourceutil.PodRequestsAndLimits(&pod)

		for resourceName, quantity := range totalRequested {
			if existingQty, exists := nodeInfo.availableResource[resourceName]; exists {
				existingQty.Sub(quantity)
				nodeInfo.availableResource[resourceName] = existingQty
			} else {
				klog.Warningf("pod %s requests unknown resource %s on node %s", pod.Name, resourceName, nodeName)
			}
		}
	}

	// Initialize result
	result := &v1.ClusterResources{
		Allocatable: &v1.ResourceInfo{
			CPU:               0,
			Memory:            0,
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
		Available: &v1.ResourceInfo{
			CPU:               0,
			Memory:            0,
			AcceleratorGroups: make(map[v1.AcceleratorType]*v1.AcceleratorGroup),
		},
	}

	resourceParserMap := c.acceleratorMgr.GetAllParsers()

	for nodeName, nodeInfo := range nodeResources {
		allocatableCPU := nodeInfo.allocatableResource[corev1.ResourceCPU]
		allocatableMemory := nodeInfo.allocatableResource[corev1.ResourceMemory]
		result.Allocatable.CPU += allocatableCPU.AsApproximateFloat64()
		result.Allocatable.Memory += allocatableMemory.AsApproximateFloat64()

		availableCPU := nodeInfo.availableResource[corev1.ResourceCPU]
		availableMemory := nodeInfo.availableResource[corev1.ResourceMemory]
		result.Available.CPU += availableCPU.AsApproximateFloat64()
		result.Available.Memory += availableMemory.AsApproximateFloat64()

		klog.V(4).Infof("Node %s allocatable resources: %+v", nodeName, nodeInfo.allocatableResource)
		klog.V(4).Infof("Node %s available resources: %+v", nodeName, nodeInfo.availableResource)

		for _, parser := range resourceParserMap {
			match := false

			accelInfo, err := parser.ParseFromKubernetes(nodeInfo.allocatableResource, nodeInfo.labels)
			if err != nil {
				return nil, fmt.Errorf("failed to parse allocatable resources from Kubernetes: %w", err)
			}

			if accelInfo != nil && len(accelInfo.AcceleratorGroups) > 0 {
				match = true

				for key, group := range accelInfo.AcceleratorGroups {
					if existingGroup, exists := result.Allocatable.AcceleratorGroups[key]; exists {
						existingGroup.Quantity += group.Quantity
						for productKey, quantity := range group.ProductGroups {
							if existingQuantity, exists := existingGroup.ProductGroups[productKey]; exists {
								existingGroup.ProductGroups[productKey] = existingQuantity + quantity
							} else {
								existingGroup.ProductGroups[productKey] = quantity
							}
						}

						result.Allocatable.AcceleratorGroups[key] = existingGroup
					} else {
						result.Allocatable.AcceleratorGroups[key] = accelInfo.AcceleratorGroups[key]
					}
				}
			}

			accelInfo, err = parser.ParseFromKubernetes(nodeInfo.availableResource, nodeInfo.labels)
			if err != nil {
				return nil, fmt.Errorf("failed to parse available resources from Kubernetes: %w", err)
			}

			if accelInfo != nil && len(accelInfo.AcceleratorGroups) > 0 {
				match = true

				for key, group := range accelInfo.AcceleratorGroups {
					if existingGroup, exists := result.Available.AcceleratorGroups[key]; exists {
						existingGroup.Quantity += group.Quantity
						for productKey, quantity := range group.ProductGroups {
							if existingQuantity, exists := existingGroup.ProductGroups[productKey]; exists {
								existingGroup.ProductGroups[productKey] = existingQuantity + quantity
							} else {
								existingGroup.ProductGroups[productKey] = quantity
							}
						}

						result.Available.AcceleratorGroups[key] = existingGroup
					} else {
						result.Available.AcceleratorGroups[key] = accelInfo.AcceleratorGroups[key]
					}
				}
			}

			if match {
				break
			}
		}
	}

	if result.Allocatable.CPU < 0 {
		result.Allocatable.CPU = 0
	} else {
		result.Allocatable.CPU = roundFloat64ToTwoDecimals(result.Allocatable.CPU)
	}

	if result.Allocatable.Memory < 0 {
		result.Allocatable.Memory = 0
	} else {
		result.Allocatable.Memory = roundFloat64ToTwoDecimals(result.Allocatable.Memory / plugin.BytesPerGiB)
	}

	if result.Available.CPU < 0 {
		result.Available.CPU = 0
	} else {
		result.Available.CPU = roundFloat64ToTwoDecimals(result.Available.CPU)
	}

	if result.Available.Memory < 0 {
		result.Available.Memory = 0
	} else {
		result.Available.Memory = roundFloat64ToTwoDecimals(result.Available.Memory / plugin.BytesPerGiB)
	}

	return result, nil
}
