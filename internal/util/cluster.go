package util

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"

	"github.com/pkg/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	pkgutil "github.com/neutree-ai/neutree/pkg/util"
)

func GetClusterModelCache(c v1.Cluster) ([]v1.ModelCache, error) {
	if c.Spec == nil {
		return nil, nil
	}

	if c.Spec.Config == nil {
		return nil, nil
	}

	// ModelCaches is now directly in ClusterConfig
	return c.Spec.Config.ModelCaches, nil
}

func ParseSSHClusterConfig(cluster *v1.Cluster) (*v1.RaySSHProvisionClusterConfig, error) {
	if cluster.Spec.Config == nil || cluster.Spec.Config.SSHConfig == nil {
		return nil, errors.New("ssh cluster config is empty")
	}

	return cluster.Spec.Config.SSHConfig, nil
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
	return pkgutil.GetClientFromCluster(cluster)
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
