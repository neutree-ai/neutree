package cluster

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/pkg/errors"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	defaultWorkdir             = "/home/ray"
	defaultModelCacheMountPath = defaultWorkdir + "/.neutree/model-cache"

	ImagePullSecretName = "image-pull-secret"
)

var (
	ErrImageNotFound     = errors.New("image not found")
	ErrorRayNodeNotFound = errors.New("ray node not found")
)

var (
	scheme = runtime.NewScheme()
	_      = rayv1.AddToScheme(scheme)
	_      = appsv1.AddToScheme(scheme)
	_      = corev1.AddToScheme(scheme)
)

type ClusterReconcile interface {
	Reconcile(ctx context.Context, cluster *v1.Cluster) error
	ReconcileDelete(ctx context.Context, cluster *v1.Cluster) error
}

type ClusterManager interface {
	ConnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint) error
	DisconnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint) error

	UpCluster(ctx context.Context, restart bool) (string, error)
	DownCluster(ctx context.Context) error
	StartNode(ctx context.Context, nodeIP string) error
	StopNode(ctx context.Context, nodeIP string) error
	GetDesireStaticWorkersIP(ctx context.Context) []string
	Sync(ctx context.Context) error
}

type dependencyValidateFunc func() error

func validateImageRegistryFunc(imageRegistry *v1.ImageRegistry) dependencyValidateFunc {
	return func() error {
		if imageRegistry.Spec.URL == "" {
			return errors.New("image registry url is empty")
		}

		if imageRegistry.Spec.Repository == "" {
			return errors.New("image registry repository is empty")
		}

		if imageRegistry.Status == nil || imageRegistry.Status.Phase != v1.ImageRegistryPhaseCONNECTED {
			return errors.New("image registry " + imageRegistry.Metadata.Name + " not connected")
		}

		return nil
	}
}

func validateClusterImageFunc(imageService registry.ImageService, registryAuth v1.ImageRegistryAuthConfig, image string) dependencyValidateFunc {
	return func() error {
		imageExisted, err := imageService.CheckImageExists(image, authn.FromConfig(authn.AuthConfig{
			Username:      registryAuth.Username,
			Password:      registryAuth.Password,
			Auth:          registryAuth.Auth,
			IdentityToken: registryAuth.IdentityToken,
			RegistryToken: registryAuth.IdentityToken,
		}))

		if err != nil {
			return err
		}

		if !imageExisted {
			return errors.Wrap(ErrImageNotFound, "image "+image+" not found")
		}

		return nil
	}
}

func GetImagePrefix(imageRegistry *v1.ImageRegistry) (string, error) {
	return getImagePrefix(imageRegistry)
}

func getImagePrefix(imageRegistry *v1.ImageRegistry) (string, error) {
	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	return registryURL.Host + "/" + imageRegistry.Spec.Repository, nil
}

func getBaseImage(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry) (string, error) {
	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	return registryURL.Host + "/" + imageRegistry.Spec.Repository + "/neutree-serve:" + cluster.Spec.Version, nil
}

func generateRayClusterMetricsScrapeTargetsConfig(cluster *v1.Cluster, dashboardService dashboard.DashboardService) (*v1.MetricsScrapeTargetsConfig, error) {
	nodes, err := dashboardService.ListNodes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list ray nodes")
	}

	metricsScrapeTargetConfig := &v1.MetricsScrapeTargetsConfig{
		Labels: map[string]string{
			"ray_io_cluster": cluster.Metadata.Name,
			"job":            "ray",
		},
	}

	for _, node := range nodes {
		if node.Raylet.IsHeadNode {
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.DashboardMetricsPort))
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.AutoScaleMetricsPort))
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.RayletMetricsPort))

			continue
		}

		if node.Raylet.State == v1.AliveNodeState {
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.RayletMetricsPort))
		}
	}

	return metricsScrapeTargetConfig, nil
}

func parseSSHClusterConfig(cluster *v1.Cluster) (*v1.RaySSHProvisionClusterConfig, error) {
	if cluster.Spec.Config == nil {
		return nil, errors.New("cluster config is empty")
	}

	config := cluster.Spec.Config

	configString, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}

	sshClusterConfig := &v1.RaySSHProvisionClusterConfig{}

	err = json.Unmarshal(configString, sshClusterConfig)
	if err != nil {
		return nil, err
	}

	return sshClusterConfig, nil
}

func parseRayKubernetesClusterConfig(cluster *v1.Cluster) (*v1.RayKubernetesProvisionClusterConfig, error) {
	if cluster.Spec.Config == nil {
		return nil, errors.New("cluster config is empty")
	}

	config := cluster.Spec.Config

	configString, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}

	kubernetesClusterConfig := &v1.RayKubernetesProvisionClusterConfig{}

	err = json.Unmarshal(configString, kubernetesClusterConfig)
	if err != nil {
		return nil, err
	}

	return kubernetesClusterConfig, nil
}

func ParseKubernetesClusterConfig(cluster *v1.Cluster) (*v1.KubernetesClusterConfig, error) {
	return parseKubernetesClusterConfig(cluster)
}

func parseKubernetesClusterConfig(cluster *v1.Cluster) (*v1.KubernetesClusterConfig, error) {
	if cluster.Spec.Config == nil {
		return nil, errors.New("cluster config is empty")
	}

	config := cluster.Spec.Config

	configString, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}

	kubernetesClusterConfig := &v1.KubernetesClusterConfig{}

	err = json.Unmarshal(configString, kubernetesClusterConfig)
	if err != nil {
		return nil, err
	}

	return kubernetesClusterConfig, nil
}

func getCtrlClientFromKubeConfig(kubeConfig string) (client.Client, error) {
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeConfig))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create REST config")
	}
	// Increase QPS and Burst to handle more requests
	// This is important for clusters with many nodes/pods
	// to avoid throttling issues
	restConfig.QPS = 10
	restConfig.Burst = 20

	ctrClient, err := client.New(restConfig, client.Options{
		Scheme: scheme,
	})

	if err != nil {
		return nil, errors.Wrap(err, "failed to create controller client")
	}

	return ctrClient, nil
}

func generateInstallNs(cluster *v1.Cluster) *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: util.ClusterNamespace(cluster),
		},
	}
}

func generateImagePullSecret(ns string, imageRegistry *v1.ImageRegistry) (*corev1.Secret, error) {
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
			Name:      ImagePullSecretName,
			Namespace: ns,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(dockerAuthData),
		},
	}, nil
}

func getUsedImageRegistries(cluster *v1.Cluster, s storage.Storage) (*v1.ImageRegistry, error) {
	imageRegistryFilter := []storage.Filter{
		{
			Column:   "metadata->name",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Spec.ImageRegistry),
		},
	}

	if cluster.Metadata.Workspace != "" {
		imageRegistryFilter = append(imageRegistryFilter, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Metadata.Workspace),
		})
	}

	imageRegistryList, err := s.ListImageRegistry(storage.ListOption{Filters: imageRegistryFilter})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list image registry")
	}

	if len(imageRegistryList) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	return &imageRegistryList[0], nil
}
