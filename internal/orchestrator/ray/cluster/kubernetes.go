package cluster

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"

	"github.com/pkg/errors"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	kuberayutil "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	"go.openly.dev/pointy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"
	"github.com/neutree-ai/neutree/internal/nfs"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/command_runner"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/internal/util"
)

const (
	ResourceSkipPatchAnnotation = "neutree.io/skip-patch"
)

const (
	nvidiaGPUResourceName = "nvidia.com/gpu"
)

var _ ClusterManager = &kubeRayClusterManager{}

var (
	scheme = runtime.NewScheme()
	_      = rayv1.AddToScheme(scheme)
	_      = appsv1.AddToScheme(scheme)
	_      = corev1.AddToScheme(scheme)
)

type kubeRayClusterManager struct {
	cluster *v1.Cluster

	kubeconfig       string
	clusterNamespace string
	installObjects   []client.Object
	ctrClient        client.Client
}

func NewKubeRayClusterManager(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry, imageService registry.ImageService,
	metricsRemoteWriteURL string) (*kubeRayClusterManager, error) {
	c := &kubeRayClusterManager{
		installObjects:   []client.Object{},
		cluster:          cluster,
		clusterNamespace: Namespace(cluster),
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

	c.kubeconfig = string(kubeconfigContent)

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigContent)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create REST config")
	}

	restConfig.QPS = 10
	restConfig.Burst = 20

	ctrClient, err := client.New(restConfig, client.Options{
		Scheme: scheme,
	})

	if err != nil {
		return nil, errors.Wrap(err, "failed to create controller client")
	}

	c.ctrClient = ctrClient

	// generate install objects
	c.installObjects = append(c.installObjects, generateInstallNs(cluster))

	imagePullSecret, err := generateImagePullSecret(cluster, imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate image pull secret")
	}

	c.installObjects = append(c.installObjects, imagePullSecret)

	vmAgentConfigMap, vmAgentScrapeConfigMap, vmAgentDeployment, err := generateVMAgent(cluster, imageRegistry, metricsRemoteWriteURL)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate vm agent")
	}

	c.installObjects = append(c.installObjects, vmAgentConfigMap, vmAgentScrapeConfigMap, vmAgentDeployment)

	kuberayCluster, err := generateKubeRayCluster(cluster, imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate kuberay cluster")
	}

	c.installObjects = append(c.installObjects, kuberayCluster)
	for i := range c.installObjects {
		addMetedataForObject(c.installObjects[i], cluster)
	}

	return c, nil
}

func (c *kubeRayClusterManager) Sync(ctx context.Context) error {
	_, err := c.UpCluster(ctx, false)
	if err != nil {
		return errors.Wrap(err, "failed to sync cluster")
	}

	err = c.syncMetricsConfig(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to sync metrics config")
	}

	return nil
}

func (c *kubeRayClusterManager) UpCluster(ctx context.Context, restart bool) (string, error) {
	for _, object := range c.installObjects {
		err := CreateOrPatch(ctx, object, c.ctrClient)
		if err != nil {
			return "", errors.Wrap(err, "failed to create or patch object "+client.ObjectKeyFromObject(object).String())
		}
	}

	return c.getClusterAccessIP(ctx)
}

func (c *kubeRayClusterManager) DownCluster(ctx context.Context) error {
	resourceExist := false

	for _, object := range c.installObjects {
		err := c.ctrClient.Get(ctx, client.ObjectKeyFromObject(object), object)
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

		err = c.ctrClient.Delete(ctx, object)
		if err != nil {
			return errors.Wrap(err, "failed to delete object "+client.ObjectKeyFromObject(object).String())
		}
	}

	if resourceExist {
		return errors.New("wait for resources to be deleted")
	}

	return nil
}

func (c *kubeRayClusterManager) GetDashboardService(ctx context.Context) (dashboard.DashboardService, error) {
	accessIP, err := c.getClusterAccessIP(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cluster access ip")
	}

	dashboardUrl := fmt.Sprintf("http://%s:8265", accessIP)

	return dashboard.NewDashboardService(dashboardUrl), nil
}

func (c *kubeRayClusterManager) GetServeEndpoint(ctx context.Context) (string, error) {
	accessIP, err := c.getClusterAccessIP(ctx)
	if err != nil {
		return "", errors.Wrap(err, "failed to get cluster access ip")
	}

	return fmt.Sprintf("http://%s:8000", accessIP), nil
}

func (c *kubeRayClusterManager) GetDesireStaticWorkersIP(ctx context.Context) []string {
	// always return null, kuberay cluster only support auto scale
	return []string{}
}

func (c *kubeRayClusterManager) StartNode(ctx context.Context, nodeIP string) error {
	// not implemented
	return nil
}

func (c *kubeRayClusterManager) StopNode(ctx context.Context, nodeIP string) error {
	// not implemented
	return nil
}

func (c *kubeRayClusterManager) getClusterAccessIP(ctx context.Context) (string, error) {
	rayCluster := &rayv1.RayCluster{}

	err := c.ctrClient.Get(ctx, client.ObjectKey{
		Name:      c.cluster.Metadata.Name,
		Namespace: c.clusterNamespace,
	}, rayCluster)
	if err != nil {
		return "", errors.Wrap(err, "failed to get ray cluster")
	}

	if rayCluster.Spec.HeadGroupSpec.ServiceType == corev1.ServiceTypeLoadBalancer {
		headSvc := &corev1.Service{}

		err := c.ctrClient.Get(ctx, client.ObjectKey{
			Name:      getHeadSvcName(rayCluster.Name),
			Namespace: c.clusterNamespace,
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

func (c *kubeRayClusterManager) ConnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint) error {
	podList := &corev1.PodList{}

	err := c.ctrClient.List(ctx, podList, client.InNamespace(c.clusterNamespace), client.MatchingLabels{
		"ray.io/cluster": c.cluster.Metadata.Name,
	})
	if err != nil {
		return errors.Wrap(err, "failed to list pods")
	}

	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		err := c.connectEndpointModel(ctx, modelRegistry, endpoint, pod.Name)
		if err != nil {
			return errors.Wrap(err, "failed to connect endpoint model")
		}
	}

	return nil
}

func (c *kubeRayClusterManager) connectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint, podName string) error {
	klog.V(4).Infof("Connect endpoint %s model to pod %s", endpoint.Metadata.Name, podName)

	if modelRegistry.Spec.Type == v1.HuggingFaceModelRegistryType {
		return nil
	}

	commandRunner := command_runner.NewKubernetesCommandRunner(c.kubeconfig, podName, c.clusterNamespace, "ray-container")

	if modelRegistry.Spec.Type == v1.BentoMLModelRegistryType {
		modelRegistryURL, err := url.Parse(modelRegistry.Spec.Url)
		if err != nil {
			return errors.Wrapf(err, "failed to parse model registry url: %s", modelRegistry.Spec.Url)
		}

		if modelRegistryURL.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			err = nfs.NewKubernetesNfsMounter(*commandRunner).
				MountNFS(ctx, modelRegistryURL.Host+modelRegistryURL.Path, filepath.Join("/mnt", endpoint.Key(), modelRegistry.Key(), endpoint.Spec.Model.Name))
			if err != nil {
				return errors.Wrap(err, "failed to mount nfs")
			}

			return nil
		}

		return fmt.Errorf("unsupported model registry type %s and scheme %s", modelRegistry.Spec.Type, modelRegistryURL.Scheme)
	}

	return fmt.Errorf("unsupported model registry type %s", modelRegistry.Spec.Type)
}

func (c *kubeRayClusterManager) DisconnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint) error {
	podList := &corev1.PodList{}
	err := c.ctrClient.List(ctx, podList, client.InNamespace(c.clusterNamespace), client.MatchingLabels{
		"ray.io/cluster": c.cluster.Metadata.Name,
	})

	if err != nil {
		return errors.Wrap(err, "failed to list pods")
	}

	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		err := c.disconnectEndpointModel(ctx, modelRegistry, endpoint, pod.Name)
		if err != nil {
			return errors.Wrap(err, "failed to disconnect endpoint model")
		}
	}

	return nil
}

func (c *kubeRayClusterManager) disconnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint, podName string) error {
	klog.V(4).Infof("Disconnect endpoint %s model from pod %s", endpoint.Metadata.Name, podName)

	if modelRegistry.Spec.Type == v1.HuggingFaceModelRegistryType {
		return nil
	}

	commandRunner := command_runner.NewKubernetesCommandRunner(c.kubeconfig, podName, c.clusterNamespace, "ray-container")

	if modelRegistry.Spec.Type == v1.BentoMLModelRegistryType {
		modelRegistryURL, err := url.Parse(modelRegistry.Spec.Url)
		if err != nil {
			return errors.Wrapf(err, "failed to parse model registry url: %s", modelRegistry.Spec.Url)
		}

		if modelRegistryURL.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			err = nfs.NewKubernetesNfsMounter(*commandRunner).
				Unmount(ctx, filepath.Join("/mnt", endpoint.Key(), modelRegistry.Key(), endpoint.Spec.Model.Name))
			if err != nil {
				return errors.Wrap(err, "failed to mount nfs")
			}

			return nil
		}

		return fmt.Errorf("unsupported model registry type %s and scheme %s", modelRegistry.Spec.Type, modelRegistryURL.Scheme)
	}

	return fmt.Errorf("unsupported model registry type %s", modelRegistry.Spec.Type)
}

func (c *kubeRayClusterManager) syncMetricsConfig(ctx context.Context) error {
	dashboardService, err := c.GetDashboardService(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get dashboard service")
	}

	clusterMetricsConfig, err := generateRayClusterMetricsScrapeTargetsConfig(c.cluster, dashboardService)
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
			Namespace: c.clusterNamespace,
		},
	}

	err = c.ctrClient.Get(ctx, client.ObjectKeyFromObject(vmAgentScrapeConfigMap), vmAgentScrapeConfigMap)
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

	err = c.ctrClient.Update(ctx, vmAgentScrapeConfigMap)
	if err != nil {
		return errors.Wrap(err, "failed to update vmagent scrape config map")
	}

	return nil
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

	userName := removeEscapes(imageRegistry.Spec.AuthConfig.Username)
	password = removeEscapes(password)
	auth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s",
		userName,
		password)))

	dockerAuthData := fmt.Sprintf(`{
			"auths": {
				"%s": {
					"username": "%s",
					"password": "%s",
					"auth": "%s"
				}
			}
		}`, registryURL.Host,
		userName,
		password,
		auth)

	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "neutree-cluster-" + cluster.Metadata.Name + "-image-pull-secret",
			Namespace: Namespace(cluster),
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(dockerAuthData),
		},
	}, nil
}

func generateVMAgent(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry, //nolint:funlen
	metricsRemoteWriteURL string) (*corev1.ConfigMap, *corev1.ConfigMap, *appsv1.Deployment, error) {
	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	vmAgentImage := registryURL.Host + "/" + imageRegistry.Spec.Repository + "/victoriametrics/vmagent:" + constants.VictoriaMetricsVersion
	vmAgentConfigMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmagent-config",
			Namespace: Namespace(cluster),
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
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmagent-scrape-config",
			Namespace: Namespace(cluster),
			Annotations: map[string]string{
				ResourceSkipPatchAnnotation: "true",
			},
		},
	}
	vmAgentDeployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmagent",
			Namespace: Namespace(cluster),
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
									Name:      "vmagent-scrape-config",
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
							Name: "vmagent-scrape-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "vmagent-scrape-config",
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

	clusterImage, err := getClusterImage(cluster, imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cluster image")
	}

	rayCluster := &rayv1.RayCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RayCluster",
			APIVersion: rayv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Metadata.Name,
			Namespace: Namespace(cluster),
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
	needNvidiaGPU := false

	resourceList := corev1.ResourceList{}
	for k, v := range spec.Resources {
		resourceList[corev1.ResourceName(k)] = resource.MustParse(v)

		if k == nvidiaGPUResourceName && v != "0" {
			needNvidiaGPU = true
		}
	}

	workerStartRayCommands := fmt.Sprintf(`python /home/ray/start.py %s --block --metrics-export-port=%d --disable-usage-stats --labels='{"%s":"%s","%s":"%s"}'`,
		getHeadSvcName(clusterName), v1.RayletMetricsPort, v1.NeutreeNodeProvisionTypeLabel, v1.StaticNodeProvisionType, v1.NeutreeServingVersionLabel, clusterVersion)

	podTemplate := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				// overwrite the container cmd to start ray worker
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
					Name:            "ray-container",
					ImagePullPolicy: corev1.PullIfNotPresent,
					Image:           clusterImage,
					Resources: corev1.ResourceRequirements{
						Requests: resourceList,
						Limits:   resourceList,
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: pointy.Bool(true),
						Capabilities: &corev1.Capabilities{
							Add: []corev1.Capability{
								"SYS_ADMIN",
							},
						},
						AllowPrivilegeEscalation: pointy.Bool(true),
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

	if !needNvidiaGPU {
		podTemplate.Spec.Containers[0].Env = append(podTemplate.Spec.Containers[0].Env, corev1.EnvVar{
			Name:  "NVIDIA_VISIBLE_DEVICES",
			Value: "void",
		})
	}

	return podTemplate
}

func buildHeadPodTemplateSpec(spec v1.HeadNodeSpec, clusterName string, clusterImage string, clusterVersion string) corev1.PodTemplateSpec {
	needNvidiaGPU := false

	resourceList := corev1.ResourceList{}
	for k, v := range spec.Resources {
		resourceList[corev1.ResourceName(k)] = resource.MustParse(v)

		if k == nvidiaGPUResourceName && v != "0" {
			needNvidiaGPU = true
		}
	}

	headStartCommand := fmt.Sprintf(`ray start --num-cpus=0 --disable-usage-stats --head --block --metrics-export-port=%d --port=6379 --object-manager-port=8076 --no-monitor --dashboard-host=0.0.0.0 --labels='{"%s":"%s"}'`, //nolint:lll
		v1.RayletMetricsPort, v1.NeutreeServingVersionLabel, clusterVersion)

	podTemplate := corev1.PodTemplateSpec{
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
					Name:            "ray-container",
					ImagePullPolicy: corev1.PullIfNotPresent,
					Image:           clusterImage,
					Resources: corev1.ResourceRequirements{
						Requests: resourceList,
						Limits:   resourceList,
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: pointy.Bool(true),
						Capabilities: &corev1.Capabilities{
							Add: []corev1.Capability{
								"SYS_ADMIN",
							},
						},
						AllowPrivilegeEscalation: pointy.Bool(true),
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

	if !needNvidiaGPU {
		podTemplate.Spec.Containers[0].Env = append(podTemplate.Spec.Containers[0].Env, corev1.EnvVar{
			Name:  "NVIDIA_VISIBLE_DEVICES",
			Value: "void",
		})
	}

	return podTemplate
}

func generateInstallNs(cluster *v1.Cluster) *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: Namespace(cluster),
		},
	}
}

func getHeadSvcName(clusterName string) string {
	return fmt.Sprintf("%s-%s-%s", clusterName, rayv1.HeadNode, "svc")
}

func addMetedataForObject(obj client.Object, cluster *v1.Cluster) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	labels[v1.NeutreeClusterLabelKey] = cluster.Metadata.Name
	labels[v1.NeutreeClusterWorkspaceLabelKey] = cluster.Metadata.Workspace
	obj.SetLabels(labels)
}

func removeEscapes(s string) string {
	re := regexp.MustCompile(`\\`)
	return re.ReplaceAllString(s, "")
}

func Namespace(cluster *v1.Cluster) string {
	return "neutree-cluster-" + util.HashString(cluster.Key())
}

func CreateOrPatch(ctx context.Context, obj client.Object, ctrClient client.Client) error {
	curObj := &unstructured.Unstructured{}
	curObj.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())

	err := ctrClient.Get(ctx, client.ObjectKeyFromObject(obj), curObj)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrClient.Create(ctx, obj)
		}

		return errors.Wrap(err, "failed to get object")
	}

	if obj.GetAnnotations() != nil && obj.GetAnnotations()[ResourceSkipPatchAnnotation] != "" {
		return nil
	}
	// patch the object
	patch := client.MergeFrom(curObj.DeepCopy())

	obj.SetAnnotations(curObj.GetAnnotations())
	obj.SetLabels(curObj.GetLabels())
	obj.SetUID(curObj.GetUID())
	obj.SetResourceVersion(curObj.GetResourceVersion())

	err = ctrClient.Patch(ctx, obj, patch)
	if err != nil {
		return errors.Wrap(err, "failed to patch object")
	}

	return nil
}
