package cluster

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

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
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"
	"github.com/neutree-ai/neutree/internal/util"
)

func (c *kubeRayClusterReconciler) generateVMAgent(reconcileCtx *ReconcileContext) (*corev1.ConfigMap, *corev1.ConfigMap, *appsv1.Deployment, error) {
	registryURL, err := url.Parse(reconcileCtx.ImageRegistry.Spec.URL)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "failed to parse image registry url "+reconcileCtx.ImageRegistry.Spec.URL)
	}

	vmAgentImage := registryURL.Host + "/" + reconcileCtx.ImageRegistry.Spec.Repository + "/vmagent:" + constants.VictoriaMetricsVersion
	vmAgentConfigMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmagent-config",
			Namespace: reconcileCtx.clusterNamespace,
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
			Name:      DefaultMonitorCollectConfigMapName,
			Namespace: reconcileCtx.clusterNamespace,
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
			Namespace: reconcileCtx.clusterNamespace,
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
							Name: ImagePullSecretName,
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "vmagent",
							Image: vmAgentImage,
							Args: []string{
								"--promscrape.config=/etc/prometheus/prometheus.yml",
								"--promscrape.configCheckInterval=10s",
								"--remoteWrite.url=" + c.metricsRemoteWriteURL,
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
										Name: DefaultMonitorCollectConfigMapName,
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

func (c *kubeRayClusterReconciler) generateKubeRayCluster(reconcileCtx *ReconcileContext) (*rayv1.RayCluster, error) {
	imagePrefix, err := getImagePrefix(reconcileCtx.ImageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image prefix")
	}

	rayCluster := &rayv1.RayCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RayCluster",
			APIVersion: rayv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      reconcileCtx.Cluster.Metadata.Name,
			Namespace: reconcileCtx.clusterNamespace,
		},
		Spec: rayv1.RayClusterSpec{
			EnableInTreeAutoscaling: pointy.Bool(true),
			AutoscalerOptions: &rayv1.AutoscalerOptions{
				Image: pointy.String(imagePrefix + "/neutree-serve:" + reconcileCtx.Cluster.Spec.Version),
			},
		},
	}

	headPodTemplate, err := c.buildHeadPodTemplateSpec(reconcileCtx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build head pod template spec")
	}

	rayCluster.Spec.HeadGroupSpec = rayv1.HeadGroupSpec{
		RayStartParams: map[string]string{},
		Template:       headPodTemplate,
	}

	if reconcileCtx.kubernetesClusterConfig.HeadNodeSpec.AccessMode == v1.KubernetesAccessModeLoadBalancer {
		rayCluster.Spec.HeadGroupSpec.ServiceType = corev1.ServiceTypeLoadBalancer
	} else {
		return nil, errors.New("unsupported access mode")
	}

	var workGroupSpecs []rayv1.WorkerGroupSpec

	for _, workerGroup := range reconcileCtx.kubernetesClusterConfig.WorkerGroupSpecs {
		workerGroupPodTemplate, err := c.buildWorkerPodTemplateSpec(reconcileCtx, workerGroup)
		if err != nil {
			return nil, errors.Wrap(err, "failed to build worker pod template spec")
		}

		workGroupSpecs = append(workGroupSpecs, rayv1.WorkerGroupSpec{
			GroupName:      workerGroup.GroupName,
			MinReplicas:    &workerGroup.MinReplicas,
			MaxReplicas:    &workerGroup.MaxReplicas,
			RayStartParams: map[string]string{},
			Template:       workerGroupPodTemplate,
		})
	}

	rayCluster.Spec.WorkerGroupSpecs = workGroupSpecs

	return rayCluster, nil
}

func (c *kubeRayClusterReconciler) buildWorkerPodTemplateSpec(reconcileCtx *ReconcileContext, spec v1.WorkerGroupSpec) (corev1.PodTemplateSpec, error) {
	resourceList := corev1.ResourceList{}

	for k, v := range spec.Resources {
		q, err := resource.ParseQuantity(v)
		if err != nil {
			return corev1.PodTemplateSpec{}, errors.Wrap(err, "failed to parse resource quantity")
		}

		resourceList[corev1.ResourceName(k)] = q
	}

	imagePrefix, err := getImagePrefix(reconcileCtx.ImageRegistry)
	if err != nil {
		return corev1.PodTemplateSpec{}, errors.Wrapf(err, "failed to get image prefix")
	}

	workerStartRayCommands := fmt.Sprintf(`python /home/ray/start.py --address=%s:6379`+
		` --block --metrics-export-port=%d --disable-usage-stats --labels='{"%s":"%s","%s":"%s"}'`,
		getHeadSvcName(reconcileCtx.Cluster.Metadata.Name),
		v1.RayletMetricsPort, v1.NeutreeNodeProvisionTypeLabel, v1.StaticNodeProvisionType, v1.NeutreeServingVersionLabel, reconcileCtx.Cluster.Spec.Version)

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
					Name: ImagePullSecretName,
				},
			},
			Containers: []corev1.Container{
				{
					Name:            "ray-container",
					ImagePullPolicy: corev1.PullIfNotPresent,
					Image:           imagePrefix + "/neutree-serve:" + reconcileCtx.Cluster.Spec.Version,
					Env: []corev1.EnvVar{
						{
							Name:  "RAY_kill_child_processes_on_worker_exit_with_raylet_subreaper",
							Value: "true",
						},
					},
					Resources: corev1.ResourceRequirements{
						Requests: resourceList,
						Limits:   resourceList,
					},
					SecurityContext: &corev1.SecurityContext{
						// Privileged: pointy.Bool(true),
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

	c.mutateModelCaches(reconcileCtx, &podTemplate)

	err = c.mutateContainerAcceleratorRuntimeConfig(reconcileCtx, &podTemplate.Spec.Containers[0])
	if err != nil {
		return corev1.PodTemplateSpec{}, errors.Wrap(err, "failed to mutate container accelerator runtime config")
	}

	return podTemplate, nil
}

func (c *kubeRayClusterReconciler) mutateModelCaches(reconcileCtx *ReconcileContext, podTemplate *corev1.PodTemplateSpec) {
	modelCaches := reconcileCtx.kubernetesClusterConfig.ModelCaches
	if modelCaches == nil {
		return
	}

	var modifyPermissionCommands []string
	modifyPermissionContainer := corev1.Container{
		Name:  "update-model-cache-permission",
		Image: podTemplate.Spec.Containers[0].Image,
		Command: []string{
			"/bin/bash",
			"-c",
		},
		SecurityContext: &corev1.SecurityContext{
			Privileged: pointy.Bool(true),
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{},
	}

	for _, modelCache := range modelCaches {
		volumeName := fmt.Sprintf("%s-model-cache", modelCache.ModelRegistryType)
		mountPath := path.Join(defaultModelCacheMountPath, string(modelCache.ModelRegistryType))
		var env corev1.EnvVar

		switch modelCache.ModelRegistryType {
		case v1.HuggingFaceModelRegistryType:
			env = corev1.EnvVar{
				Name:  v1.HFHomeEnv,
				Value: mountPath,
			}
		case v1.BentoMLModelRegistryType:
			env = corev1.EnvVar{
				Name:  v1.BentoMLHomeEnv,
				Value: mountPath,
			}
		default:
			klog.Warningf("Model registry type %s is not supported, skip", modelCache.ModelRegistryType)
			continue
		}

		volume := corev1.Volume{
			Name: volumeName,
		}

		volumeMount := corev1.VolumeMount{
			Name:      volumeName,
			MountPath: mountPath,
		}

		if modelCache.HostPath != nil {
			hostPathType := corev1.HostPathDirectoryOrCreate
			volume.VolumeSource = corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: modelCache.HostPath.Path,
					Type: &hostPathType,
				},
			}
		} else if modelCache.NFS != nil {
			volume.VolumeSource = corev1.VolumeSource{
				NFS: &corev1.NFSVolumeSource{
					Server:   modelCache.NFS.Server,
					Path:     modelCache.NFS.Path,
					ReadOnly: modelCache.NFS.ReadOnly,
				},
			}
		}

		for i := range podTemplate.Spec.Containers {
			// append volume mount to container.
			podTemplate.Spec.Containers[i].VolumeMounts = append(podTemplate.Spec.Containers[i].VolumeMounts, volumeMount)
			podTemplate.Spec.Containers[i].Env = append(podTemplate.Spec.Containers[i].Env, env)
		}

		podTemplate.Spec.Volumes = append(podTemplate.Spec.Volumes, volume)
		modifyPermissionContainer.VolumeMounts = append(modifyPermissionContainer.VolumeMounts, volumeMount)
		modifyPermissionCommands = append(modifyPermissionCommands, "sudo chown -R $(id -u):$(id -g) "+mountPath)
	}

	modifyPermissionContainer.Command = append(modifyPermissionContainer.Command, strings.Join(modifyPermissionCommands, "&&"))
	// add init container to update model cache permission to the same user as the ray worker process.
	podTemplate.Spec.InitContainers = append(podTemplate.Spec.InitContainers, modifyPermissionContainer)
}

func (c *kubeRayClusterReconciler) buildHeadPodTemplateSpec(reconcileCtx *ReconcileContext) (corev1.PodTemplateSpec, error) {
	resourceList := corev1.ResourceList{}
	for k, v := range reconcileCtx.kubernetesClusterConfig.HeadNodeSpec.Resources {
		resourceList[corev1.ResourceName(k)] = resource.MustParse(v)
	}

	imagePrefix, err := getImagePrefix(reconcileCtx.ImageRegistry)
	if err != nil {
		return corev1.PodTemplateSpec{}, errors.Wrap(err, "failed to get cluster image")
	}

	headStartCommand := fmt.Sprintf(`python /home/ray/start.py --head --port=6379 --num-cpus=0 --disable-usage-stats --block --metrics-export-port=%d --no-monitor --dashboard-host=0.0.0.0 --labels='{"%s":"%s"}'`, //nolint:lll
		v1.RayletMetricsPort, v1.NeutreeServingVersionLabel, reconcileCtx.Cluster.Spec.Version)
	image := imagePrefix + "/neutree-serve:" + reconcileCtx.Cluster.Spec.Version
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
					Name: ImagePullSecretName,
				},
			},
			Containers: []corev1.Container{
				{
					Name:            "ray-container",
					ImagePullPolicy: corev1.PullIfNotPresent,
					Image:           image,
					Env: []corev1.EnvVar{
						{
							Name:  "RAY_kill_child_processes_on_worker_exit_with_raylet_subreaper",
							Value: "true",
						},
					},
					Resources: corev1.ResourceRequirements{
						Requests: resourceList,
						Limits:   resourceList,
					},
					SecurityContext: &corev1.SecurityContext{
						// Privileged: pointy.Bool(true),
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

	err = c.mutateContainerAcceleratorRuntimeConfig(reconcileCtx, &podTemplate.Spec.Containers[0])
	if err != nil {
		return corev1.PodTemplateSpec{}, errors.Wrap(err, "failed to mutate container accelerator runtime config")
	}

	return podTemplate, nil
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
