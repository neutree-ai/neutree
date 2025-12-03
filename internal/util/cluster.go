package util

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"

	"github.com/pkg/errors"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var (
	scheme = runtime.NewScheme()
	_      = rayv1.AddToScheme(scheme)
	_      = appsv1.AddToScheme(scheme)
	_      = corev1.AddToScheme(scheme)
)

func GetClusterModelCache(c v1.Cluster) ([]v1.ModelCache, error) {
	if c.Spec == nil {
		return nil, nil
	}

	if c.Spec.Config == nil {
		return nil, nil
	}

	// Access the appropriate config based on cluster type
	if c.Spec.Config.SSHConfig != nil {
		return c.Spec.Config.SSHConfig.ModelCaches, nil
	}

	if c.Spec.Config.KubernetesConfig != nil {
		return c.Spec.Config.KubernetesConfig.ModelCaches, nil
	}

	return nil, nil
}

func ParseSSHClusterConfig(cluster *v1.Cluster) (*v1.RaySSHProvisionClusterConfig, error) {
	if cluster.Spec.Config == nil || cluster.Spec.Config.SSHConfig == nil {
		return nil, errors.New("ssh cluster config is empty")
	}

	return cluster.Spec.Config.SSHConfig, nil
}

// Deprecated: ParseRayKubernetesClusterConfig is deprecated.
// RayKubernetesProvisionClusterConfig is not part of the ClusterConfig structure.
// This function is kept for backward compatibility but should not be used.
func ParseRayKubernetesClusterConfig(cluster *v1.Cluster) (*v1.RayKubernetesProvisionClusterConfig, error) {
	return nil, errors.New("ParseRayKubernetesClusterConfig is deprecated: RayKubernetesProvisionClusterConfig is not available in ClusterConfig structure")
}

func ParseKubernetesClusterConfig(c *v1.Cluster) (*v1.KubernetesClusterConfig, error) {
	if c.Spec.Config == nil || c.Spec.Config.KubernetesConfig == nil {
		return nil, errors.New("kubernetes cluster config is empty")
	}

	return c.Spec.Config.KubernetesConfig, nil
}

func GetKubeConfigFromCluster(cluster *v1.Cluster) (string, error) {
	config, err := ParseKubernetesClusterConfig(cluster)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse kubernetes cluster config")
	}

	if config.Kubeconfig == "" {
		return "", errors.New("kubeconfig is empty")
	}

	kubeconfigContent, err := base64.StdEncoding.DecodeString(config.Kubeconfig)
	if err != nil {
		return "", errors.Wrap(err, "failed to decode kubeconfig")
	}

	return string(kubeconfigContent), nil
}

func GetClientSetFromCluster(cluster *v1.Cluster) (*kubernetes.Clientset, error) {
	kubeconfig, err := GetKubeConfigFromCluster(cluster)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get kubeconfig from cluster")
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create REST config")
	}

	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create kubernetes clientset")
	}

	return clientSet, nil
}

func GetClientFromCluster(cluster *v1.Cluster) (client.Client, error) {
	kubeconfig, err := GetKubeConfigFromCluster(cluster)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get kubeconfig from cluster")
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
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

func ClusterNamespace(cluster *v1.Cluster) string {
	return "neutree-cluster-" + HashString(cluster.Key())
}

func GetApiServerUrlFromKubeConfig(kubeconfig string) (string, error) {
	kubeconfigContent, err := base64.StdEncoding.DecodeString(kubeconfig)
	if err != nil {
		return "", errors.Wrap(err, "failed to decode kubeconfig")
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigContent)
	if err != nil {
		return "", errors.Wrap(err, "failed to create REST config from kubeconfig")
	}

	return restConfig.Host, nil
}

func GetApiServerUrlFromDecodedKubeConfig(kubeconfigContent string) (string, error) {
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigContent))
	if err != nil {
		return "", errors.Wrap(err, "failed to create REST config from kubeconfig")
	}

	return restConfig.Host, nil
}

func GetClusterServeAddress(cluster *v1.Cluster) (string, string, int, error) {
	if cluster.Status == nil || cluster.Status.DashboardURL == "" {
		return "", "", 0, errors.New("cluster status or dashboard URL is empty")
	}

	urlParse, err := url.Parse(cluster.Status.DashboardURL)
	if err != nil {
		return "", "", 0, errors.Wrapf(err, "failed to parse dashboard url")
	}

	if urlParse.Host == "" || urlParse.Scheme == "" {
		return "", "", 0, errors.New("failed to get host or scheme from dashboard url")
	}

	port := 8000
	if urlParse.Port() != "" {
		port, err = strconv.Atoi(urlParse.Port())
		if err != nil {
			return "", "", 0, errors.Wrapf(err, "failed to parse port from dashboard url")
		}
	}

	if cluster.Spec.Type == v1.SSHClusterType {
		port = 8000
	}

	return urlParse.Scheme, urlParse.Hostname(), port, nil
}

func CacheName(cache v1.ModelCache) string {
	baseName := "models-cache"

	if cache.Name != "" {
		baseName = baseName + "-" + cache.Name
	}

	return baseName
}

func GetTransportFromDecodedKubeConfig(kubeconfigContent string) (http.RoundTripper, error) {
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigContent))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create REST config from kubeconfig")
	}

	transport, err := rest.TransportFor(restConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create transport from REST config")
	}

	return transport, nil
}
