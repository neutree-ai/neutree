package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"

	sigyaml "sigs.k8s.io/yaml"
)

const (
	modelCacheMountPathPrefix = "/root/.neutree/model-cache/"
)

type kubernetesOrchestrator struct {
	storage storage.Storage

	manager cluster.ClusterManager
	// Implementation details would go here
}

func newKubernetesOrchestrator(opts Options, manager cluster.ClusterManager) (*kubernetesOrchestrator, error) {
	return &kubernetesOrchestrator{
		storage: opts.Storage,
		manager: manager,
	}, nil
}

func (k *kubernetesOrchestrator) CreateCluster() (string, error) {
	// Implementation for creating a Kubernetes cluster
	return k.manager.UpCluster(context.Background(), false)
}

func (k *kubernetesOrchestrator) DeleteCluster() error {
	// Implementation for deleting a Kubernetes cluster
	return k.manager.DownCluster(context.Background())
}

func (k *kubernetesOrchestrator) SyncCluster() error {
	// Implementation for syncing a Kubernetes cluster
	_, err := k.manager.UpCluster(context.Background(), false)
	return err
}

func (k *kubernetesOrchestrator) StartNode(nodeIP string) error {
	// Implementation for starting a node in a Kubernetes cluster
	return nil
}

func (k *kubernetesOrchestrator) StopNode(nodeIP string) error {
	// Implementation for stopping a node in a Kubernetes cluster
	return nil
}

func (k *kubernetesOrchestrator) GetDesireStaticWorkersIP() []string {
	// Implementation for getting desired static worker IPs in a Kubernetes cluster
	return nil
}

func (k *kubernetesOrchestrator) HealthCheck() error {
	// Implementation for health checking a Kubernetes cluster
	return nil
}

func (k *kubernetesOrchestrator) ClusterStatus() (*v1.RayClusterStatus, error) {
	// Implementation for getting the status of a Kubernetes cluster
	return &v1.RayClusterStatus{}, nil
}

func (k *kubernetesOrchestrator) ListNodes() ([]v1.NodeSummary, error) {
	// Implementation for listing nodes in a Kubernetes cluster
	return nil, nil
}

func (k *kubernetesOrchestrator) CreateEndpoint(endpoint *v1.Endpoint) (*v1.EndpointStatus, error) {
	deployedCluster, err := getEndpointDeployCluster(k.storage, endpoint)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get deploy cluster for endpoint %s", endpoint.Metadata.Name)
	}

	if deployedCluster.Spec.Type != "kubernetes" {
		return nil, errors.Errorf("endpoint %s deploy cluster %s is not kubernetes type", endpoint.Metadata.Name, deployedCluster.Metadata.Name)
	}

	imageRegistry, err := getRelateImageRegistry(k.storage, deployedCluster)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get relate image registry for cluster %s", deployedCluster.Metadata.Name)
	}

	engine, err := getUsedEngine(k.storage, endpoint)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get engine for endpoint %s", endpoint.Metadata.Name)
	}

	modelRegistry, err := getEndpointModelRegistry(k.storage, endpoint)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get model registry for endpoint %s", endpoint.Metadata.Name)
	}

	data, err := k.buildManifestData(endpoint, deployedCluster, modelRegistry, engine, imageRegistry)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to build manifest data for endpoint %s", endpoint.Metadata.Name)
	}

	// now we only use deployment to deploy endpoint,
	// but in the future, we may support more deploy mode, like distribute inference or pd inference,
	// and then, we may expand the manifest data and templates.
	dep, err := buildDeployment(data)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to build deployment for endpoint %s", endpoint.Metadata.Name)
	}

	ctrlClient, err := util.GetClientFromCluster(deployedCluster)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get kubernetes client for cluster %s", deployedCluster.Metadata.Name)
	}

	err = ctrlClient.Patch(context.Background(), dep, client.Apply, client.ForceOwnership, client.FieldOwner("neutree"))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to patch deployment for endpoint %s", endpoint.Metadata.Name)
	}

	// Implementation for creating an endpoint in a Kubernetes cluster
	return k.GetEndpointStatus(endpoint)
}

func (k *kubernetesOrchestrator) buildManifestData(endpoint *v1.Endpoint, deployedCluster *v1.Cluster, modelRegistry *v1.ModelRegistry, engine *v1.Engine, imageRegistry *v1.ImageRegistry) (DeploymentManifestData, error) {
	// Build and return the manifest data for the endpoint deployment
	imagePrefix, err := util.GetImagePrefix(imageRegistry)
	if err != nil {
		return DeploymentManifestData{}, errors.Wrapf(err, "failed to get image prefix for image registry %s", imageRegistry.Metadata.Name)
	}

	data := DeploymentManifestData{
		EndpointName:    endpoint.Metadata.Name,
		Namespace:       util.ClusterNamespace(deployedCluster),
		ClusterName:     deployedCluster.Metadata.Name,
		Workspace:       deployedCluster.Metadata.Workspace,
		EngineName:      engine.Metadata.Name,
		EngineVersion:   endpoint.Spec.Engine.Version,
		ImagePrefix:     imagePrefix,
		ImagePullSecret: cluster.ImagePullSecretName,
		Replicas:        int32(*endpoint.Spec.Replicas.Num),
		RoutingLogic:    "roundrobin",
	}

	if endpoint.Spec.DeploymentOptions != nil && endpoint.Spec.DeploymentOptions["scheduler"] != nil {
		scheduleConfig, ok := endpoint.Spec.DeploymentOptions["scheduler"].(map[string]interface{})
		if ok {
			if logic, exists := scheduleConfig["type"].(string); exists {
				data.RoutingLogic = logic
			}
		}
	}

	task := "generate"

	modelArgs := map[string]interface{}{
		"registry_type": modelRegistry.Spec.Type,
		"name":          endpoint.Spec.Model.Name,
		"file":          endpoint.Spec.Model.File,
		"version":       endpoint.Spec.Model.Version,
		"task":          task,
	}

	data.ModelArgs = modelArgs

	if endpoint.Spec.Variables != nil {
		if v, ok := endpoint.Spec.Variables["engine_args"].(map[string]interface{}); ok {
			data.EngineArgs = v
		}
	}

	resource := map[string]string{}
	if endpoint.Spec.Resources.CPU != nil {
		resource["cpu"] = fmt.Sprintf("%f", *endpoint.Spec.Resources.CPU)
	}

	if endpoint.Spec.Resources.Memory != nil {
		resource["memory"] = fmt.Sprintf("%fGi", *endpoint.Spec.Resources.Memory)
	}

	if endpoint.Spec.Resources.GPU != nil {
		resource["nvidia.com/gpu"] = fmt.Sprintf("%f", *endpoint.Spec.Resources.GPU)
	}

	// todo: handle accelerator resources
	data.Resources = resource

	modelCaches, err := util.GetClusterModelCache(*deployedCluster)
	if err != nil {
		return DeploymentManifestData{}, errors.Wrapf(err, "failed to get model cache for cluster %s", deployedCluster.Metadata.Name)
	}

	env := map[string]string{}

	if len(modelCaches) > 0 {
		volumes, volumeMounts, modelEnv := generateModelCacheConfig(modelCaches)
		data.Volumes = volumes
		data.VolumeMounts = volumeMounts
		maps.Copy(env, modelEnv)
	}

	data.Env = env

	switch modelRegistry.Spec.Type {
	case v1.BentoMLModelRegistryType:
		url, _ := url.Parse(modelRegistry.Spec.Url) // nolint: errcheck
		// todo: support local file type env set
		mountPath := filepath.Join("/mnt", modelRegistry.Key(), endpoint.Spec.Model.Name)
		if url != nil && url.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			env[v1.BentoMLHomeEnv] = mountPath
		}
		data.Volumes = append(data.Volumes, corev1.Volume{
			Name: "bentoml-model-registry",
			VolumeSource: corev1.VolumeSource{
				NFS: &corev1.NFSVolumeSource{
					Server: url.Host,
					Path:   url.Path,
				},
			},
		})

		data.VolumeMounts = append(data.VolumeMounts, corev1.VolumeMount{
			Name:      "bentoml-model-registry",
			MountPath: mountPath,
		})
	case v1.HuggingFaceModelRegistryType:
		env[v1.HFEndpoint] = strings.TrimSuffix(modelRegistry.Spec.Url, "/")
		if modelRegistry.Spec.Credentials != "" {
			env[v1.HFTokenEnv] = modelRegistry.Spec.Credentials
		}
	}

	return data, nil
}

func buildDeployment(data DeploymentManifestData) (client.Object, error) {
	return renderManifest(demoDeploymentTemplate, data)
}

func renderManifest(templateStr string, data DeploymentManifestData) (client.Object, error) {
	tmpl, err := template.New("manifest").Funcs(sprig.TxtFuncMap()).Funcs(template.FuncMap{
		"toYaml": toYAML,
	}).Parse(templateStr)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse template")
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, errors.Wrap(err, "failed to execute template")
	}

	fmt.Println(buf.String())

	// Decode YAML to unstructured object
	obj := &unstructured.Unstructured{}
	decoder := yaml.NewYAMLOrJSONDecoder(&buf, 4096)
	if err := decoder.Decode(obj); err != nil {
		return nil, errors.Wrap(err, "failed to decode manifest")
	}

	return obj, nil
}

func toYAML(v interface{}) string {
	data, err := sigyaml.Marshal(v)
	if err != nil {
		// Swallow errors inside of a template.
		return ""
	}
	return strings.TrimSuffix(string(data), "\n")
}

func generateModelCacheConfig(modelCaches []v1.ModelCache) ([]corev1.Volume, []corev1.VolumeMount, map[string]string) {
	volumes := []corev1.Volume{}
	volumeMounts := []corev1.VolumeMount{}
	env := make(map[string]string)

	for _, cache := range modelCaches {
		name := fmt.Sprintf("%s-model-cache", cache.ModelRegistryType)
		if cache.HostPath != nil {
			volumes = append(volumes, corev1.Volume{
				Name: name,
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: cache.HostPath.Path,
					},
				},
			})
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      name,
				MountPath: path.Join(modelCacheMountPathPrefix, name),
			})

			if cache.ModelRegistryType == v1.BentoMLModelRegistryType {
				env[v1.BentoMLHomeEnv] = path.Join(modelCacheMountPathPrefix, name)
			} else if cache.ModelRegistryType == v1.HuggingFaceModelRegistryType {
				env[v1.HFHomeEnv] = path.Join(modelCacheMountPathPrefix, name)
			}
		}

		if cache.NFS != nil {
			volumes = append(volumes, corev1.Volume{
				Name: name,
				VolumeSource: corev1.VolumeSource{
					NFS: &corev1.NFSVolumeSource{
						Server: cache.NFS.Server,
						Path:   cache.NFS.Path,
					},
				},
			})

			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      name,
				MountPath: path.Join(modelCacheMountPathPrefix, name),
			})
		}
	}

	return volumes, volumeMounts, env
}

type DeploymentManifestData struct {
	EndpointName    string
	Namespace       string
	ClusterName     string
	Workspace       string
	EngineName      string
	EngineVersion   string
	ImagePrefix     string
	ImagePullSecret string
	ModelArgs       map[string]interface{}
	EngineArgs      map[string]interface{}
	Resources       map[string]string
	Env             map[string]string
	Volumes         []corev1.Volume
	VolumeMounts    []corev1.VolumeMount
	RoutingLogic    string
	Replicas        int32
	NodeSelector    map[string]string
}

func (k *kubernetesOrchestrator) DeleteEndpoint(endpoint *v1.Endpoint) error {
	deployedCluster, err := getEndpointDeployCluster(k.storage, endpoint)
	if err != nil {
		if err != storage.ErrResourceNotFound {
			return errors.Wrapf(err, "failed to get deploy cluster for endpoint %s", endpoint.Metadata.Name)
		}
		// If the deployed cluster is not found, we assume the endpoint does not exist.
		return nil
	}

	if deployedCluster.Spec.Type != "kubernetes" {
		return errors.Errorf("endpoint %s deploy cluster %s is not kubernetes type", endpoint.Metadata.Name, deployedCluster.Metadata.Name)
	}

	ctrlClient, err := util.GetClientFromCluster(deployedCluster)
	if err != nil {
		return errors.Wrapf(err, "failed to get kubernetes client for cluster %s", deployedCluster.Metadata.Name)
	}

	err = ctrlClient.Delete(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: endpoint.Metadata.Name, Namespace: util.ClusterNamespace(deployedCluster)}})
	if err != nil {

		if apierrors.IsNotFound(err) {
			// If the deployment is not found, we assume it has already been deleted.
			return nil
		}

		return errors.Wrapf(err, "failed to delete deployment for endpoint %s", endpoint.Metadata.Name)
	}

	// Implementation for deleting an endpoint in a Kubernetes cluster
	return fmt.Errorf("waiting for endpoint %s to be fully deleted", endpoint.Metadata.Name)
}

func (k *kubernetesOrchestrator) GetEndpointStatus(endpoint *v1.Endpoint) (*v1.EndpointStatus, error) {
	deployedCluster, err := getEndpointDeployCluster(k.storage, endpoint)
	if err != nil {

		return nil, errors.Wrapf(err, "failed to get deploy cluster for endpoint %s", endpoint.Metadata.Name)
	}

	if deployedCluster.Spec.Type != "kubernetes" {
		return nil, errors.Errorf("endpoint %s deploy cluster %s is not kubernetes type", endpoint.Metadata.Name, deployedCluster.Metadata.Name)
	}

	ctrlClient, err := util.GetClientFromCluster(deployedCluster)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get kubernetes client for cluster %s", deployedCluster.Metadata.Name)
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      endpoint.Metadata.Name,
			Namespace: util.ClusterNamespace(deployedCluster),
		},
	}

	err = ctrlClient.Get(context.Background(), client.ObjectKeyFromObject(dep), dep)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get deployment for endpoint %s", endpoint.Metadata.Name)
	}

	status := &v1.EndpointStatus{}

	if dep.Status.ReadyReplicas == dep.Status.Replicas && dep.Status.UpdatedReplicas == dep.Status.Replicas {
		status.Phase = v1.EndpointPhaseRUNNING
	} else {
		status.Phase = v1.EndpointPhaseFAILED
		errorMessage := ""
		for _, condtion := range dep.Status.Conditions {
			if condtion.Status == corev1.ConditionTrue {
				continue
			}
			errorMessage += fmt.Sprintf("Type: %s, Reason: %s, Message: %s; ", condtion.Type, condtion.Reason, condtion.Message)
		}

		status.ErrorMessage = errorMessage
	}

	// Implementation for getting the status of an	 endpoint in a Kubernetes cluster
	return status, nil
}

func (k *kubernetesOrchestrator) ConnectEndpointModel(endpoint *v1.Endpoint) error {
	// Implementation for connecting a model to an endpoint in a Kubernetes cluster
	return nil
}

func (k *kubernetesOrchestrator) DisconnectEndpointModel(endpoint *v1.Endpoint) error {
	// Implementation for disconnecting a model from an endpoint in a Kubernetes cluster
	return nil
}
