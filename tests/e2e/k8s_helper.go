package e2e

import (
	"context"
	"encoding/base64"
	"os"
	"time"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

// K8sHelper provides Kubernetes client operations for e2e tests.
type K8sHelper struct {
	clientset *kubernetes.Clientset
}

// NewK8sHelper creates a K8sHelper from a base64-encoded kubeconfig.
func NewK8sHelper(kubeconfigBase64 string) *K8sHelper {
	kubeconfigBytes, err := base64.StdEncoding.DecodeString(kubeconfigBase64)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to decode kubeconfig base64")

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create REST config from kubeconfig")

	clientset, err := kubernetes.NewForConfig(restConfig)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create kubernetes clientset")

	return &K8sHelper{clientset: clientset}
}

// ClusterNamespace returns the namespace for a Neutree cluster.
func ClusterNamespace(workspace, clusterName string, clusterID int) string {
	c := v1.Cluster{
		ID:       clusterID,
		Metadata: &v1.Metadata{Name: clusterName, Workspace: workspace},
	}

	return util.ClusterNamespace(&c)
}

// GetNamespace retrieves a namespace by name.
func (h *K8sHelper) GetNamespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	return h.clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
}

// ListDeployments lists deployments in a namespace with optional label selector.
func (h *K8sHelper) ListDeployments(ctx context.Context, namespace, labelSelector string) ([]appsv1.Deployment, error) {
	list, err := h.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}

	return list.Items, nil
}

// GetDeployment retrieves a specific deployment.
func (h *K8sHelper) GetDeployment(ctx context.Context, namespace, name string) (*appsv1.Deployment, error) {
	return h.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
}

// ListServices lists services in a namespace with optional label selector.
func (h *K8sHelper) ListServices(ctx context.Context, namespace, labelSelector string) ([]corev1.Service, error) {
	list, err := h.clientset.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}

	return list.Items, nil
}

// ListSecrets lists secrets in a namespace.
func (h *K8sHelper) ListSecrets(ctx context.Context, namespace, labelSelector string) ([]corev1.Secret, error) {
	list, err := h.clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}

	return list.Items, nil
}

// ListConfigMaps lists configmaps in a namespace.
func (h *K8sHelper) ListConfigMaps(ctx context.Context, namespace, labelSelector string) ([]corev1.ConfigMap, error) {
	list, err := h.clientset.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}

	return list.Items, nil
}

// ListPVCs lists persistent volume claims in a namespace.
func (h *K8sHelper) ListPVCs(ctx context.Context, namespace, labelSelector string) ([]corev1.PersistentVolumeClaim, error) {
	list, err := h.clientset.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}

	return list.Items, nil
}

// NamespaceExists checks if a namespace exists.
func (h *K8sHelper) NamespaceExists(ctx context.Context, name string) bool {
	_, err := h.GetNamespace(ctx, name)
	if err == nil {
		return true
	}

	if apierrors.IsNotFound(err) {
		return false
	}

	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "unexpected error checking namespace %s", name)

	return false
}

// WaitForNamespaceDeleted waits until a namespace no longer exists.
func (h *K8sHelper) WaitForNamespaceDeleted(ctx context.Context, name string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if !h.NamespaceExists(ctx, name) {
			return
		}

		time.Sleep(5 * time.Second)
	}

	ExpectWithOffset(1, h.NamespaceExists(ctx, name)).To(BeFalse(),
		"namespace %s should be deleted within %s", name, timeout)
}

// NewK8sHelperFromFile creates a K8sHelper from a kubeconfig file path.
func NewK8sHelperFromFile(kubeconfigPath string) *K8sHelper {
	data, err := os.ReadFile(kubeconfigPath)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to read kubeconfig file %s", kubeconfigPath)

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(data)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create REST config from kubeconfig")

	clientset, err := kubernetes.NewForConfig(restConfig)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create kubernetes clientset")

	return &K8sHelper{clientset: clientset}
}

// ListPods lists pods in a namespace with optional label selector.
func (h *K8sHelper) ListPods(ctx context.Context, namespace, labelSelector string) ([]corev1.Pod, error) {
	list, err := h.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}

	return list.Items, nil
}
