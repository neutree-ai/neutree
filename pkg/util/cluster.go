package util

import (
	"encoding/base64"

	"github.com/pkg/errors"
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

var (
	controllerClientScheme = runtime.NewScheme()
	_                      = admissionregistrationv1.AddToScheme(controllerClientScheme)
	_                      = appsv1.AddToScheme(controllerClientScheme)
	_                      = corev1.AddToScheme(controllerClientScheme)
	_                      = rbacv1.AddToScheme(controllerClientScheme)
)

// GetClientFromCluster creates a controller-runtime client from a cluster's kubeconfig.
func GetClientFromCluster(cluster *v1.Cluster) (client.Client, error) {
	restConfig, err := restConfigFromCluster(cluster)
	if err != nil {
		return nil, err
	}

	controllerClient, err := client.New(restConfig, client.Options{
		Scheme: controllerClientScheme,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create controller client")
	}

	return controllerClient, nil
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

	restConfig.QPS = 10
	restConfig.Burst = 20

	return restConfig, nil
}

func kubeconfigFromCluster(cluster *v1.Cluster) (string, error) {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.Config == nil || cluster.Spec.Config.KubernetesConfig == nil {
		return "", errors.Wrap(errors.New("kubernetes cluster config is empty"), "failed to parse kubernetes cluster config")
	}

	kubeconfig := cluster.Spec.Config.KubernetesConfig.Kubeconfig
	if kubeconfig == "" {
		return "", errors.New("kubeconfig is empty")
	}

	kubeconfigContent, err := base64.StdEncoding.DecodeString(kubeconfig)
	if err != nil {
		return "", errors.Wrap(err, "failed to decode kubeconfig")
	}

	return string(kubeconfigContent), nil
}
