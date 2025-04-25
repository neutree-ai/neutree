package cluster

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/pkg/errors"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	kuberayutil "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	rayclientv1 "github.com/ray-project/kuberay/ray-operator/pkg/client/clientset/versioned/typed/ray/v1"
	"go.openly.dev/pointy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var _ ClusterManager = &kubeRayClusterManager{}

type kubeRayClusterManager struct {
	imageService registry.ImageService

	kuberayCluster  *rayv1.RayCluster
	imagePullSecret *corev1.Secret

	clientSet *kubernetes.Clientset
	rayClient *rayclientv1.RayV1Client

	vmAgentConfigMap       *corev1.ConfigMap
	vmAgentScrapeConfigMap *corev1.ConfigMap
	vmAgentDeployment      *appsv1.Deployment
}

func NewKubeRayClusterManager(MetricsRemoteWriteURL string, cluster *v1.Cluster, imageRegistry *v1.ImageRegistry, imageService registry.ImageService) (*kubeRayClusterManager, error) {
	c := &kubeRayClusterManager{
		imageService: imageService,
	}

	err := checkClusterImage(imageService, cluster, imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to check cluster image")
	}

	kubeconfig, err := getKubeconfig(cluster)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get kubeconfig")
	}

	if kubeconfig == "" {
		return nil, errors.New("kubeconfig is required")
	}

	kubeconfigContent, err := base64.StdEncoding.DecodeString(kubeconfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to decode kubeconfig")
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigContent)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create REST config")
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create kubernetes client")
	}

	c.clientSet = clientset

	rayClient, err := rayclientv1.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create ray client")
	}

	c.rayClient = rayClient

	c.imagePullSecret, err = generateImagePullSecret(cluster, imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate image pull secret")
	}

	c.vmAgentConfigMap, c.vmAgentScrapeConfigMap, c.vmAgentDeployment, err = generateVMAgent(cluster, imageRegistry, MetricsRemoteWriteURL)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate vm agent")
	}

	c.kuberayCluster, err = generateKubeRayCluster(cluster, imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate kuberay cluster")
	}

	return c, nil
}

func (c *kubeRayClusterManager) Sync(ctx context.Context) error {
	_, err := c.UpCluster(ctx, false)
	if err != nil {
		return errors.Wrap(err, "failed to sync cluster")
	}

	return nil
}

func (c *kubeRayClusterManager) UpCluster(ctx context.Context, restart bool) (string, error) {
	err := c.createNs(ctx)
	if err != nil {
		return "", errors.Wrap(err, "failed to create namespace")
	}

	err = c.createOrUpdateImagePullSecret(ctx)
	if err != nil {
		return "", errors.Wrap(err, "failed to create or update image pull secret")
	}

	err = c.createOrUpdateVMAgent(ctx)
	if err != nil {
		return "", errors.Wrap(err, "failed to create vm agent")
	}

	err = c.createOrUpdateKubeRayCluster(ctx)
	if err != nil {
		return "", errors.Wrap(err, "failed to create or update kuberay cluster")
	}

	return c.GetHeadIP(ctx)
}

func (c *kubeRayClusterManager) createOrUpdateVMAgent(ctx context.Context) error {
	vmAgentConfigMap, err := c.clientSet.CoreV1().ConfigMaps(c.kuberayCluster.Namespace).Get(ctx, c.vmAgentConfigMap.Name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to get vm agent config map")
		}
		_, err = c.clientSet.CoreV1().ConfigMaps(c.kuberayCluster.Namespace).Create(ctx, c.vmAgentConfigMap, metav1.CreateOptions{})
		if err != nil {
			return errors.Wrap(err, "failed to create vm agent config map")
		}
	}

	vmAgentConfigMap.Data = c.vmAgentConfigMap.Data
	_, err = c.clientSet.CoreV1().ConfigMaps(c.kuberayCluster.Namespace).Update(ctx, vmAgentConfigMap, metav1.UpdateOptions{})
	if err != nil {
		return errors.Wrap(err, "failed to update vm agent config map")
	}

	vmAgentScrapeConfigMap, err := c.clientSet.CoreV1().ConfigMaps(c.kuberayCluster.Namespace).Get(ctx, c.vmAgentScrapeConfigMap.Name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to get vm agent scrape config map")
		}
		_, err = c.clientSet.CoreV1().ConfigMaps(c.kuberayCluster.Namespace).Create(ctx, c.vmAgentScrapeConfigMap, metav1.CreateOptions{})
		if err != nil {
			return errors.Wrap(err, "failed to create vm agent scrape config map")
		}
	}

	scrapConfig, err := generateRayClusterMetricsScrapeTargetsConfig(c.kuberayCluster.Name, c.kuberayCluster.Namespace, c.clientSet)
	if err != nil {
		return errors.Wrap(err, "failed to generate ray cluster metrics scrape targets config")
	}

	scrapConfigContent, err := json.Marshal([]v1.MetricsScrapeTargetsConfig{*scrapConfig})
	if err != nil {
		return errors.Wrap(err, "failed to marshal metrics scrape targets config")
	}

	vmAgentScrapeConfigMap.Data = map[string]string{
		"scrape_configs.json": string(scrapConfigContent),
	}
	_, err = c.clientSet.CoreV1().ConfigMaps(c.kuberayCluster.Namespace).Update(ctx, vmAgentScrapeConfigMap, metav1.UpdateOptions{})
	if err != nil {
		return errors.Wrap(err, "failed to update vm agent scrape config map")
	}

	vmAgentDeploy, err := c.clientSet.AppsV1().Deployments(c.kuberayCluster.Namespace).Get(ctx, c.vmAgentDeployment.Name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to get vm agent deployment")
		}
		_, err = c.clientSet.AppsV1().Deployments(c.kuberayCluster.Namespace).Create(ctx, c.vmAgentDeployment, metav1.CreateOptions{})
		if err != nil {
			return errors.Wrap(err, "failed to create vm agent deployment")
		}
	}

	vmAgentDeploy.Spec = c.vmAgentDeployment.Spec
	_, err = c.clientSet.AppsV1().Deployments(c.kuberayCluster.Namespace).Update(ctx, vmAgentDeploy, metav1.UpdateOptions{})
	if err != nil {
		return errors.Wrap(err, "failed to update vm agent deployment")
	}

	return nil
}

func (c *kubeRayClusterManager) createNs(ctx context.Context) error {
	_, err := c.clientSet.CoreV1().Namespaces().Get(ctx, c.kuberayCluster.Namespace, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to get namespace")
		}
		_, err = c.clientSet.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: c.kuberayCluster.Namespace,
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return errors.Wrap(err, "failed to create namespace")
		}
		return nil
	}

	return nil
}

func (c *kubeRayClusterManager) createOrUpdateImagePullSecret(ctx context.Context) error {
	secret, err := c.clientSet.CoreV1().Secrets(c.kuberayCluster.Namespace).Get(ctx, c.imagePullSecret.Name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to get image pull secret")
		}
		_, err = c.clientSet.CoreV1().Secrets(c.kuberayCluster.Namespace).Create(ctx, c.imagePullSecret, metav1.CreateOptions{})
		if err != nil {
			return errors.Wrap(err, "failed to create image pull secret")
		}
		return nil
	}

	secret.Data = c.imagePullSecret.Data
	_, err = c.clientSet.CoreV1().Secrets(c.kuberayCluster.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
	if err != nil {
		return errors.Wrap(err, "failed to update image pull secret")
	}
	return nil
}

func (c *kubeRayClusterManager) createOrUpdateKubeRayCluster(ctx context.Context) error {
	_, err := c.rayClient.RayClusters(c.kuberayCluster.Namespace).Get(ctx, c.kuberayCluster.Name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return errors.Wrap(err, "failed to get ray cluster")
		}
		_, err = c.rayClient.RayClusters(c.kuberayCluster.Namespace).Create(ctx, c.kuberayCluster, metav1.CreateOptions{})
		if err != nil {
			return errors.Wrap(err, "failed to create ray cluster")
		}

		return nil
	}

	// todo add update logic
	return nil
}

func (c *kubeRayClusterManager) DownCluster(ctx context.Context) error {
	err := c.deleteKubeRayCluster(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to delete kuberay cluster")
	}

	err = c.deleteImagePullSecret(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to delete image pull secret")
	}

	err = c.deleteNs(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to delete namespace")
	}

	return nil
}

func (c *kubeRayClusterManager) deleteNs(ctx context.Context) error {
	ns, err := c.clientSet.CoreV1().Namespaces().Get(ctx, c.kuberayCluster.Namespace, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}

		return errors.Wrap(err, "failed to get namespace")
	}

	if !ns.DeletionTimestamp.IsZero() {
		return errors.New("wait for namespace to be deleted")
	}

	err = c.clientSet.CoreV1().Namespaces().Delete(ctx, c.kuberayCluster.Namespace, metav1.DeleteOptions{})
	if err != nil {
		return errors.Wrap(err, "failed to delete namespace")
	}

	return errors.New("wait for namespace to be deleted")
}

func (c *kubeRayClusterManager) deleteKubeRayCluster(ctx context.Context) error {
	rayCluster, err := c.rayClient.RayClusters(c.kuberayCluster.Namespace).Get(ctx, c.kuberayCluster.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "failed to get ray cluster")
	}

	if !rayCluster.DeletionTimestamp.IsZero() {
		return errors.New("wait for ray cluster to be deleted")
	}

	err = c.rayClient.RayClusters(c.kuberayCluster.Namespace).Delete(ctx, c.kuberayCluster.Name, metav1.DeleteOptions{})
	if err != nil {
		return errors.Wrap(err, "failed to delete ray cluster")
	}

	return errors.New("wait for ray cluster to be deleted")
}

func (c *kubeRayClusterManager) deleteImagePullSecret(ctx context.Context) error {
	imagePullSecret, err := c.clientSet.CoreV1().Secrets(c.kuberayCluster.Namespace).Get(ctx, c.imagePullSecret.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "failed to get image pull secret")
	}

	if !imagePullSecret.DeletionTimestamp.IsZero() {
		return errors.New("wait for ray cluster to be deleted")
	}

	err = c.clientSet.CoreV1().Secrets(c.kuberayCluster.Namespace).Delete(ctx, c.imagePullSecret.Name, metav1.DeleteOptions{})
	if err != nil {
		return errors.Wrap(err, "failed to delete image pull secret")
	}

	return errors.New("wait for image pull secret to be deleted")
}

func (c *kubeRayClusterManager) StartNode(ctx context.Context, nodeIP string) error {
	// not implemented yet
	return nil
}

func (c *kubeRayClusterManager) StopNode(ctx context.Context, nodeIP string) error {
	// not implemented yet
	return nil
}

func (c *kubeRayClusterManager) GetHeadIP(ctx context.Context) (string, error) {
	rayCluster, err := c.rayClient.RayClusters(c.kuberayCluster.Namespace).Get(ctx, c.kuberayCluster.Name, metav1.GetOptions{})
	if err != nil {
		return "", errors.Wrap(err, "failed to get ray cluster")
	}

	if rayCluster.Status.State != rayv1.Ready {
		return "", errors.New("ray cluster is not ready")
	}

	if c.kuberayCluster.Spec.HeadGroupSpec.ServiceType == corev1.ServiceTypeLoadBalancer {
		headServiceName := getHeadSvcName(c.kuberayCluster.Name)
		service, err := c.clientSet.CoreV1().Services(c.kuberayCluster.Namespace).Get(ctx, headServiceName, metav1.GetOptions{})
		if err != nil {
			return "", errors.Wrap(err, "failed to get service")
		}

		if len(service.Status.LoadBalancer.Ingress) == 0 {
			return "", errors.New("service has no load balancer ip")
		}

		return service.Status.LoadBalancer.Ingress[0].IP, nil
	}

	return "", errors.New("no access to head node")
}

func (c *kubeRayClusterManager) DrainNode(ctx context.Context, nodeID, reason, message string, deadlineRemainSeconds int) error {
	return nil
}

func (c *kubeRayClusterManager) GetDesireStaticWorkersIP(_ context.Context) []string {
	return []string{}
}

func (c *kubeRayClusterManager) GetDashboardService(ctx context.Context) (dashboard.DashboardService, error) {
	if c.kuberayCluster.Spec.HeadGroupSpec.ServiceType == corev1.ServiceTypeLoadBalancer {
		headServiceName := getHeadSvcName(c.kuberayCluster.Name)

		service, err := c.clientSet.CoreV1().Services(c.kuberayCluster.Namespace).Get(ctx, headServiceName, metav1.GetOptions{})
		if err != nil {
			return nil, errors.Wrap(err, "failed to get service")
		}

		if len(service.Status.LoadBalancer.Ingress) == 0 {
			return nil, errors.New("service has no load balancer ip")
		}

		dashboardUrl := fmt.Sprintf("http://%s:8265", service.Status.LoadBalancer.Ingress[0].IP)
		return dashboard.NewDashboardService(dashboardUrl), nil
	}

	return nil, errors.New("dashboard service not found")
}

func (c *kubeRayClusterManager) GetServeEndpoint(ctx context.Context) (string, error) {
	if c.kuberayCluster.Spec.HeadGroupSpec.ServiceType == corev1.ServiceTypeLoadBalancer {
		serveServiceName := kuberayutil.GenerateServeServiceName(c.kuberayCluster.Name)

		service, err := c.clientSet.CoreV1().Services(c.kuberayCluster.Namespace).Get(ctx, serveServiceName, metav1.GetOptions{})
		if err != nil {
			return "", errors.Wrap(err, "failed to get service")
		}

		if len(service.Status.LoadBalancer.Ingress) == 0 {
			return "", errors.New("service has no load balancer ip")
		}

		serveURL := fmt.Sprintf("http://%s:8000", service.Status.LoadBalancer.Ingress[0].IP)
		return serveURL, nil
	}

	return "", errors.New("serve service not found")
}

func getKubeconfig(cluster *v1.Cluster) (string, error) {
	config := &v1.RayKubernetesProvisionClusterConfig{}
	configContent, err := json.Marshal(cluster.Spec.Config)
	if err != nil {
		return "", errors.Wrap(err, "failed to marshal kubernetes provision cluster config")
	}
	err = json.Unmarshal(configContent, config)
	if err != nil {
		return "", errors.Wrap(err, "failed to unmarshal kubernetes provision cluster config")
	}
	return config.Kubeconfig, nil
}

func generateImagePullSecret(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry) (*corev1.Secret, error) {
	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse image registry url: %s", imageRegistry.Spec.URL)
	}

	var password string
	switch {
	case imageRegistry.Spec.AuthConfig.Password != "":
		password = imageRegistry.Spec.AuthConfig.Password
	case imageRegistry.Spec.AuthConfig.IdentityToken != "":
		password = imageRegistry.Spec.AuthConfig.IdentityToken
	case imageRegistry.Spec.AuthConfig.RegistryToken != "":
		password = imageRegistry.Spec.AuthConfig.RegistryToken
	}

	auth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s",
		imageRegistry.Spec.AuthConfig.Username,
		password)))

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "neutree-cluster-" + cluster.Metadata.Name + "-image-pull-secret",
			Namespace: cluster.Key(),
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(fmt.Sprintf(`{
				"auths": {
					"%s": {
						"username": "%s",
						"password": "%s",
						"auth": "%s"
					}
				}
			}`, registryURL.Host,
				imageRegistry.Spec.AuthConfig.Username,
				password,
				auth)),
		},
	}, nil
}

func generateVMAgent(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry, metricsRemoteWriteURL string) (*corev1.ConfigMap, *corev1.ConfigMap, *appsv1.Deployment, error) {
	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	vmAgentImage := registryURL.Host + "/" + imageRegistry.Spec.Repository + "/victoriametrics/vmagent:" + constants.VictoriaMetricsVersion
	vmAgentConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmagent-config",
			Namespace: cluster.Key(),
		},
		Data: map[string]string{
			"prometheus.yml": `global:
  scrape_interval: 30s # Set the scrape interval to every 30 seconds. Default is every 1 minute.

scrape_configs:
# Scrape from each Ray node as defined in the service_discovery.json provided by Ray.
- job_name: 'neutree'
  file_sd_configs:
  - files:
    - '/etc/prometheus/scrape/*.json'`,
		},
	}
	vmAgentScrapeConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "scrape-config",
			Namespace: cluster.Key(),
		},
	}
	vmAgentDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmagent",
			Namespace: cluster.Key(),
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "vmagent",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "vmagent",
					},
				},
				Spec: corev1.PodSpec{
					ImagePullSecrets: []corev1.LocalObjectReference{
						{
							Name: "neutree-cluster-" + cluster.Metadata.Name + "-image-pull-secret",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "vmagent",
							Image: vmAgentImage,
							Args: []string{
								"--promscrape.config=/etc/prometheus/prometheus.yml",
								"--promscrape.configCheckInterval=10s",
								"--remoteWrite.url=" + metricsRemoteWriteURL,
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "vmagent-config",
									MountPath: "/etc/prometheus",
								},
								{
									Name:      "scrape-config",
									MountPath: "/etc/prometheus/scrape",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "vmagent-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "vmagent-config",
									},
								},
							},
						},
						{
							Name: "scrape-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "scrape-config",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	return vmAgentConfigMap, vmAgentScrapeConfigMap, vmAgentDeployment, nil
}

func generateKubeRayCluster(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry) (*rayv1.RayCluster, error) {
	config := &v1.RayKubernetesProvisionClusterConfig{}
	configContent, err := json.Marshal(cluster.Spec.Config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal kubernetes provision cluster config")
	}
	err = json.Unmarshal(configContent, config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal kubernetes provision cluster config")
	}

	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	clusterImage := registryURL.Host + "/" + imageRegistry.Spec.Repository + "/neutree-serve:" + cluster.Spec.Version

	rayCluster := &rayv1.RayCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Metadata.Name,
			Namespace: cluster.Key(),
		},
		Spec: rayv1.RayClusterSpec{
			EnableInTreeAutoscaling: pointy.Bool(true),
			AutoscalerOptions: &rayv1.AutoscalerOptions{
				Image: &clusterImage,
			},
		},
	}

	rayCluster.Spec.HeadGroupSpec = rayv1.HeadGroupSpec{
		RayStartParams: map[string]string{},
		Template:       buildHeadPodTemplateSpec(config.HeadNodeSpec, cluster.Metadata.Name, clusterImage, cluster.Spec.Version),
	}

	if config.HeadNodeSpec.AccessMode == v1.KubernetesAccessModeLoadBalancer {
		rayCluster.Spec.HeadGroupSpec.ServiceType = corev1.ServiceTypeLoadBalancer
	} else {
		return nil, errors.New("unsupported access mode")
	}

	var workGroupSpecs []rayv1.WorkerGroupSpec
	for _, workerGroup := range config.WorkerGroupSpecs {
		workGroupSpecs = append(workGroupSpecs, rayv1.WorkerGroupSpec{
			GroupName:      workerGroup.GroupName,
			MinReplicas:    &workerGroup.MinReplicas,
			MaxReplicas:    &workerGroup.MaxReplicas,
			RayStartParams: map[string]string{},
			Template:       buildWorkerPodTemplateSpec(workerGroup, cluster.Metadata.Name, clusterImage, cluster.Spec.Version),
		})
	}

	rayCluster.Spec.WorkerGroupSpecs = workGroupSpecs

	return rayCluster, nil
}

func buildWorkerPodTemplateSpec(spec v1.WorkerGroupSpec, clusterName string, clusterImage string, clusterVersion string) corev1.PodTemplateSpec {
	resourceList := corev1.ResourceList{}
	for k, v := range spec.Resources {
		resourceList[corev1.ResourceName(k)] = resource.MustParse(v)
	}

	workerStartRayCommands := fmt.Sprintf(`python /home/ray/start.py %s --block --metrics-export-port=%d --disable-usage-stats --labels='{"%s":"%s","%s":"%s"}'`,
		getHeadSvcName(clusterName), v1.RayletMetricsPort, v1.NeutreeNodeProvisionTypeLabel, v1.StaticNodeProvisionType, v1.NeutreeServingVersionLabel, clusterVersion)

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				// overwrite the container cmd to start ray head
				kuberayutil.RayOverwriteContainerCmdAnnotationKey: "true",
				// enable build serve service
				kuberayutil.EnableServeServiceKey: "true",
			},
		},
		Spec: corev1.PodSpec{
			ImagePullSecrets: []corev1.LocalObjectReference{
				{
					Name: "neutree-cluster-" + clusterName + "-image-pull-secret",
				},
			},
			Containers: []corev1.Container{
				{
					Name:            "ray-worker",
					ImagePullPolicy: corev1.PullIfNotPresent,
					Image:           clusterImage,
					Resources: corev1.ResourceRequirements{
						Requests: resourceList,
						Limits:   resourceList,
					},
					Command: []string{"/bin/bash", "-lc", "--"},
					Args:    []string{"ulimit -n 65536; " + workerStartRayCommands},
					// overwrite the metrics port
					Ports: []corev1.ContainerPort{
						{
							Name:          "metrics",
							ContainerPort: v1.RayletMetricsPort,
						},
					},
				},
			},
		},
	}
}

func buildHeadPodTemplateSpec(spec v1.HeadNodeSpec, clusterName string, clusterImage string, clusterVersion string) corev1.PodTemplateSpec {
	resourceList := corev1.ResourceList{}
	for k, v := range spec.Resources {
		resourceList[corev1.ResourceName(k)] = resource.MustParse(v)
	}

	headStartCommand := fmt.Sprintf(`ray start --disable-usage-stats --head --block --metrics-export-port=%d --port=6379 --object-manager-port=8076 --no-monitor --dashboard-host=0.0.0.0 --labels='{"%s":"%s"}'`, //nolint:lll
		v1.RayletMetricsPort, v1.NeutreeServingVersionLabel, clusterVersion)
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				// overwrite the container cmd to start ray head
				"ray.io/overwrite-container-cmd": "true",
			},
		},
		Spec: corev1.PodSpec{
			ImagePullSecrets: []corev1.LocalObjectReference{
				{
					Name: "neutree-cluster-" + clusterName + "-image-pull-secret",
				},
			},
			Containers: []corev1.Container{
				{
					Name:            "ray-head",
					ImagePullPolicy: corev1.PullIfNotPresent,
					Image:           clusterImage,
					Resources: corev1.ResourceRequirements{
						Requests: resourceList,
						Limits:   resourceList,
					},
					Command: []string{"/bin/bash", "-lc", "--"},
					Args:    []string{"ulimit -n 65536; " + headStartCommand},
					// overwrite the metrics port
					Ports: []corev1.ContainerPort{
						{
							Name:          "metrics",
							ContainerPort: v1.RayletMetricsPort,
						},
						{
							Name:          "dash-metrics",
							ContainerPort: v1.DashboardMetricsPort,
						},
						{
							Name:          "auto-metrics",
							ContainerPort: v1.AutoScaleMetricsPort,
						},
						{
							Name:          kuberayutil.ServingPortName,
							ContainerPort: kuberayutil.DefaultServingPort,
						},
						{
							Name:          kuberayutil.GcsServerPortName,
							ContainerPort: kuberayutil.DefaultGcsServerPort,
						},
						{
							Name:          kuberayutil.DashboardPortName,
							ContainerPort: kuberayutil.DefaultDashboardPort,
						},
					},
				},
			},
		},
	}
}

func getHeadSvcName(clusterName string) string {
	return fmt.Sprintf("%s-%s-%s", clusterName, rayv1.HeadNode, "svc")
}

func generateRayClusterMetricsScrapeTargetsConfig(clusterName, clusterNamespace string, kubeClient *kubernetes.Clientset) (*v1.MetricsScrapeTargetsConfig, error) {
	metricsScrapeTargetConfig := &v1.MetricsScrapeTargetsConfig{
		Labels: map[string]string{
			"ray_io_cluster": clusterName,
			"job":            "ray",
		},
	}

	headPodList, err := kubeClient.CoreV1().Pods(clusterNamespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: "ray.io/node-type=head",
	})

	if err != nil {
		return nil, errors.Wrap(err, "failed to list ray head pods")
	}

	for _, pod := range headPodList.Items {
		metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", pod.Status.PodIP, v1.DashboardMetricsPort))
		metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", pod.Status.PodIP, v1.AutoScaleMetricsPort))
		metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", pod.Status.PodIP, v1.RayletMetricsPort))
	}

	workerPodList, err := kubeClient.CoreV1().Pods(clusterNamespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: "ray.io/node-type=worker",
	})

	for _, pod := range workerPodList.Items {
		metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", pod.Status.PodIP, v1.RayletMetricsPort))
	}

	return metricsScrapeTargetConfig, nil
}
