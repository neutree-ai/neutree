package util

import (
	"encoding/base64"

	"github.com/pkg/errors"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var kubernetesScheme = func() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = rayv1.AddToScheme(scheme)
	_ = admissionregistrationv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	return scheme
}()

// GetClientFromCluster creates a controller-runtime client from a Kubernetes
// cluster's stored kubeconfig.
func GetClientFromCluster(cluster *v1.Cluster) (client.Client, error) {
	restConfig, err := restConfigFromCluster(cluster)
	if err != nil {
		return nil, err
	}

	// Increase QPS and Burst to avoid throttling for clusters with many nodes
	// or pods.
	restConfig.QPS = 10
	restConfig.Burst = 20

	ctrlClient, err := client.New(restConfig, client.Options{Scheme: kubernetesScheme})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create controller client")
	}

	return ctrlClient, nil
}

func restConfigFromCluster(cluster *v1.Cluster) (*rest.Config, error) {
	kubeconfig, err := kubeconfigFromCluster(cluster)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get kubeconfig from cluster")
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create REST config")
	}

	return restConfig, nil
}

func kubeconfigFromCluster(cluster *v1.Cluster) (string, error) {
	config, err := kubernetesClusterConfig(cluster)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse kubernetes cluster config")
	}

	kubeconfig := config.Kubeconfig
	if kubeconfig == "" {
		return "", errors.New("kubeconfig is empty")
	}

	kubeconfigContent, err := base64.StdEncoding.DecodeString(kubeconfig)
	if err != nil {
		return "", errors.Wrap(err, "failed to decode kubeconfig")
	}

	return string(kubeconfigContent), nil
}

func kubernetesClusterConfig(cluster *v1.Cluster) (*v1.KubernetesClusterConfig, error) {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.Config == nil || cluster.Spec.Config.KubernetesConfig == nil {
		return nil, errors.New("kubernetes cluster config is empty")
	}

	return cluster.Spec.Config.KubernetesConfig, nil
}
