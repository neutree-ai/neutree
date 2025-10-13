package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/pkg/errors"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	DefaultMonitorCollectConfigMapName = "vmagent-scrape-config"
)

var _ ClusterReconcile = &kubeRayClusterReconciler{}

type kubeRayClusterReconciler struct {
	acceleratorManager    accelerator.Manager
	storage               storage.Storage
	metricsRemoteWriteURL string
}

func NewKubeRayClusterReconciler(acceleratorManager accelerator.Manager, storage storage.Storage, metricsRemoteWriteURL string) *kubeRayClusterReconciler {
	c := &kubeRayClusterReconciler{
		acceleratorManager:    acceleratorManager,
		storage:               storage,
		metricsRemoteWriteURL: metricsRemoteWriteURL,
	}

	return c
}

func (c *kubeRayClusterReconciler) generateConfig(reconcileCtx *ReconcileContext) error {
	var err error

	cluster := reconcileCtx.Cluster

	config, err := util.ParseRayKubernetesClusterConfig(cluster)
	if err != nil {
		return errors.Wrap(err, "failed to parse cluster config")
	}

	reconcileCtx.rayKubernetesClusterConfig = config

	err = c.configClusterAcceleratorType(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to config cluster accelerator type")
	}

	reconcileCtx.kubeconfig, err = util.GetKubeConfigFromCluster(cluster)
	if err != nil {
		return errors.Wrap(err, "failed to get kubeconfig")
	}

	reconcileCtx.ctrClient, err = util.GetClientFromCluster(cluster)
	if err != nil {
		return errors.Wrap(err, "failed to get controller client")
	}

	// generate install objects
	reconcileCtx.installObjects = append(reconcileCtx.installObjects, generateInstallNs(cluster))

	imagePullSecret, err := generateImagePullSecret(reconcileCtx.clusterNamespace, reconcileCtx.ImageRegistry)
	if err != nil {
		return errors.Wrap(err, "failed to generate image pull secret")
	}

	reconcileCtx.installObjects = append(reconcileCtx.installObjects, imagePullSecret)

	vmAgentConfigMap, vmAgentScrapeConfigMap, vmAgentDeployment, err := c.generateVMAgent(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to generate vm agent")
	}

	reconcileCtx.installObjects = append(reconcileCtx.installObjects, vmAgentConfigMap, vmAgentScrapeConfigMap, vmAgentDeployment)

	kuberayCluster, err := c.generateKubeRayCluster(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to generate kuberay cluster")
	}

	reconcileCtx.installObjects = append(reconcileCtx.installObjects, kuberayCluster)
	for i := range reconcileCtx.installObjects {
		addMetedataForObject(reconcileCtx.installObjects[i], cluster)
	}

	return nil
}

func (c *kubeRayClusterReconciler) Reconcile(ctx context.Context, cluster *v1.Cluster) error {
	imageRegistry, err := getUsedImageRegistries(cluster, c.storage)
	if err != nil {
		return errors.Wrap(err, "failed to get used image registries")
	}

	reconcileCtx := &ReconcileContext{
		Cluster:       cluster,
		ImageRegistry: imageRegistry,
		Ctx:           ctx,

		clusterNamespace: util.ClusterNamespace(cluster),
	}

	err = c.generateConfig(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to generate config")
	}

	if reconcileCtx.Cluster.Status == nil || !reconcileCtx.Cluster.Status.Initialized {
		err = c.initialize(reconcileCtx)
		if err != nil {
			return errors.Wrap(err, "failed to initialize cluster")
		}
	}

	headIP, err := c.getClusterAccessIP(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to get cluster access ip")
	}

	reconcileCtx.rayService = dashboard.NewDashboardService(fmt.Sprintf("http://%s:8265", headIP))

	defer func() {
		err = c.setClusterStatus(reconcileCtx)
		if err != nil {
			klog.Error(err, "failed to set cluster status")
		}
	}()

	_, err = c.upCluster(reconcileCtx, false)
	if err != nil {
		return errors.Wrap(err, "failed to up cluster")
	}

	err = c.syncMetricsConfig(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to sync metrics config")
	}

	_, err = reconcileCtx.rayService.GetClusterMetadata()
	if err != nil {
		return errors.Wrap(err, "failed to get cluster metadata")
	}

	return nil
}

func (c *kubeRayClusterReconciler) setClusterStatus(reconcileCtx *ReconcileContext) error {
	clusterStatus, err := getRayClusterStatus(reconcileCtx.rayService)
	if err != nil {
		return errors.Wrap(err, "failed to get ray cluster status")
	}

	setClusterStatus(reconcileCtx.Cluster, clusterStatus)

	return nil
}

func (c *kubeRayClusterReconciler) ReconcileDelete(ctx context.Context, cluster *v1.Cluster) error {
	imageRegistry, err := getUsedImageRegistries(cluster, c.storage)
	if err != nil {
		return errors.Wrap(err, "failed to get used image registries")
	}

	reconcileCtx := &ReconcileContext{
		Cluster:          cluster,
		Ctx:              ctx,
		clusterNamespace: util.ClusterNamespace(cluster),

		ImageRegistry: imageRegistry,
	}

	err = c.generateConfig(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to generate config")
	}

	err = c.downCluster(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to reconcile delete cluster")
	}

	return nil
}

func (c *kubeRayClusterReconciler) initialize(reconcileCtx *ReconcileContext) error {
	klog.Infof("Start to initialize cluster %s", reconcileCtx.Cluster.Metadata.WorkspaceName())

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

	headIP, err := c.upCluster(reconcileCtx, false)
	if err != nil {
		return errors.Wrap(err, "failed to up cluster")
	}

	dashboardSvc := dashboard.NewDashboardService(fmt.Sprintf("http://%s:8265", headIP))

	_, err = dashboardSvc.GetClusterMetadata()
	if err != nil {
		return errors.Wrap(err, "failed to get cluster metadata")
	}

	reconcileCtx.Cluster.Status.Initialized = true
	reconcileCtx.Cluster.Status.DashboardURL = fmt.Sprintf("http://%s:8265", headIP)

	err = c.storage.UpdateCluster(strconv.Itoa(reconcileCtx.Cluster.ID), reconcileCtx.Cluster)
	if err != nil {
		return errors.Wrap(err, "failed to update cluster status")
	}

	return nil
}

func (c *kubeRayClusterReconciler) upCluster(reconcileCtx *ReconcileContext, _ bool) (string, error) {
	for _, object := range reconcileCtx.installObjects {
		err := util.CreateOrPatch(reconcileCtx.Ctx, object, reconcileCtx.ctrClient)
		if err != nil {
			return "", errors.Wrap(err, "failed to create or patch object "+client.ObjectKeyFromObject(object).String())
		}
	}

	return c.getClusterAccessIP(reconcileCtx)
}

func (c *kubeRayClusterReconciler) downCluster(reconcileCtx *ReconcileContext) error {
	resourceExist := false

	for _, object := range reconcileCtx.installObjects {
		err := reconcileCtx.ctrClient.Get(reconcileCtx.Ctx, client.ObjectKeyFromObject(object), object)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}

			return errors.Wrap(err, "failed to get object "+client.ObjectKeyFromObject(object).String())
		}

		resourceExist = true

		if object.GetDeletionTimestamp() != nil {
			continue
		}

		err = reconcileCtx.ctrClient.Delete(reconcileCtx.Ctx, object)
		if err != nil {
			return errors.Wrap(err, "failed to delete object "+client.ObjectKeyFromObject(object).String())
		}
	}

	if resourceExist {
		return errors.New("wait for resources to be deleted")
	}

	return nil
}

func (c *kubeRayClusterReconciler) syncMetricsConfig(reconcileCtx *ReconcileContext) error {
	clusterMetricsConfig, err := generateRayClusterMetricsScrapeTargetsConfig(reconcileCtx.Cluster, reconcileCtx.rayService)
	if err != nil {
		return errors.Wrap(err, "failed to generate ray cluster metrics scrape targets config")
	}

	clusterMetricsConfigContent, err := json.Marshal([]*v1.MetricsScrapeTargetsConfig{clusterMetricsConfig})
	if err != nil {
		return errors.Wrap(err, "failed to marshal ray cluster metrics config")
	}

	vmAgentScrapeConfigMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmagent-scrape-config",
			Namespace: reconcileCtx.clusterNamespace,
		},
	}

	err = reconcileCtx.ctrClient.Get(reconcileCtx.Ctx, client.ObjectKeyFromObject(vmAgentScrapeConfigMap), vmAgentScrapeConfigMap)
	if err != nil {
		return errors.Wrap(err, "failed to get vmagent scrape config map")
	}

	if vmAgentScrapeConfigMap.Data == nil {
		vmAgentScrapeConfigMap.Data = make(map[string]string)
	}

	if vmAgentScrapeConfigMap.Data["cluster.json"] == string(clusterMetricsConfigContent) {
		return nil
	}

	vmAgentScrapeConfigMap.Data["cluster.json"] = string(clusterMetricsConfigContent)

	err = reconcileCtx.ctrClient.Update(reconcileCtx.Ctx, vmAgentScrapeConfigMap)
	if err != nil {
		return errors.Wrap(err, "failed to update vmagent scrape config map")
	}

	return nil
}

func (c *kubeRayClusterReconciler) getClusterAccessIP(reconcileCtx *ReconcileContext) (string, error) {
	rayCluster := &rayv1.RayCluster{}

	err := reconcileCtx.ctrClient.Get(reconcileCtx.Ctx, client.ObjectKey{
		Name:      reconcileCtx.Cluster.Metadata.Name,
		Namespace: reconcileCtx.clusterNamespace,
	}, rayCluster)
	if err != nil {
		return "", errors.Wrap(err, "failed to get ray cluster")
	}

	if rayCluster.Spec.HeadGroupSpec.ServiceType == corev1.ServiceTypeLoadBalancer {
		headSvc := &corev1.Service{}

		err := reconcileCtx.ctrClient.Get(reconcileCtx.Ctx, client.ObjectKey{
			Name:      getHeadSvcName(rayCluster.Name),
			Namespace: reconcileCtx.clusterNamespace,
		}, headSvc)
		if err != nil {
			return "", errors.Wrap(err, "failed to get service")
		}

		if len(headSvc.Status.LoadBalancer.Ingress) == 0 {
			return "", errors.New("service has no load balancer ip")
		}

		return headSvc.Status.LoadBalancer.Ingress[0].IP, nil
	}

	return "", errors.New("only support load balancer service type")
}

func (c *kubeRayClusterReconciler) configClusterAcceleratorType(reconcileCtx *ReconcileContext) error {
	if reconcileCtx.rayKubernetesClusterConfig.AcceleratorType != nil {
		return nil
	}

	detectAcceleratorType, err := c.detectClusterAcceleratorType(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to detect cluster accelerator type")
	}

	reconcileCtx.rayKubernetesClusterConfig.AcceleratorType = &detectAcceleratorType
	reconcileCtx.Cluster.Spec.Config = reconcileCtx.rayKubernetesClusterConfig

	return c.storage.UpdateCluster(strconv.Itoa(reconcileCtx.Cluster.ID), &v1.Cluster{
		Spec: reconcileCtx.Cluster.Spec,
	})
}

func (c *kubeRayClusterReconciler) detectClusterAcceleratorType(reconcileCtx *ReconcileContext) (string, error) {
	detectAcceleratorType := ""

	for _, workerGroup := range reconcileCtx.rayKubernetesClusterConfig.WorkerGroupSpecs {
		resourceList := corev1.ResourceList{}

		for k, v := range workerGroup.Resources {
			q, err := resource.ParseQuantity(v)
			if err != nil {
				return "", errors.Wrap(err, "failed to parse resource quantity")
			}

			resourceList[corev1.ResourceName(k)] = q
		}

		acceleratorType, err := c.acceleratorManager.GetKubernetesContainerAcceleratorType(context.Background(), corev1.Container{
			Resources: corev1.ResourceRequirements{
				Requests: resourceList,
			},
		})
		if err != nil {
			return "", errors.Wrap(err, "failed to get container accelerator type")
		}

		if detectAcceleratorType == "" {
			detectAcceleratorType = acceleratorType
			continue
		}

		if acceleratorType == "" {
			continue
		}

		if detectAcceleratorType != acceleratorType {
			return "", errors.New("detect different accelerator type")
		}
	}

	return detectAcceleratorType, nil
}

func (c *kubeRayClusterReconciler) mutateContainerAcceleratorRuntimeConfig(reconcileContext *ReconcileContext, container *corev1.Container) error {
	acceleratorRuntimeConfig, err := c.acceleratorManager.GetKubernetesContainerRuntimeConfig(context.Background(),
		*reconcileContext.rayKubernetesClusterConfig.AcceleratorType, *container)
	if err != nil {
		return errors.Wrap(err, "failed to get container runtime config")
	}

	if acceleratorRuntimeConfig.ImageSuffix != "" {
		container.Image = container.Image + "-" + acceleratorRuntimeConfig.ImageSuffix
	}

	if acceleratorRuntimeConfig.Env != nil {
		for k, v := range acceleratorRuntimeConfig.Env {
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  k,
				Value: v,
			})
		}
	}

	return nil
}
