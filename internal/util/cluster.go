package util

import (
	"encoding/base64"
	"encoding/json"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/pkg/errors"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	scheme = runtime.NewScheme()
	_      = rayv1.AddToScheme(scheme)
	_      = appsv1.AddToScheme(scheme)
	_      = corev1.AddToScheme(scheme)
)

func GetClusterModelCache(c v1.Cluster) ([]v1.ModelCache, error) {
	content, err := json.Marshal(c.Spec.Config)
	if err != nil {
		return nil, err
	}
	config := v1.CommonClusterConfig{}
	err = json.Unmarshal(content, &config)
	if err != nil {
		return nil, err
	}
	return config.ModelCaches, nil
}

func ParseSSHClusterConfig(cluster *v1.Cluster) (*v1.RaySSHProvisionClusterConfig, error) {
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

func ParseKubernetesClusterConfig(c v1.Cluster) (*v1.KubernetesClusterConfig, error) {
	if c.Spec.Config == nil {
		return nil, errors.New("cluster config is empty")
	}

	content, err := json.Marshal(c.Spec.Config)
	if err != nil {
		return nil, err
	}
	config := v1.KubernetesClusterConfig{}
	err = json.Unmarshal(content, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func GetKubeConfigFromCluster(cluster *v1.Cluster) (string, error) {
	config, err := ParseKubernetesClusterConfig(*cluster)
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
