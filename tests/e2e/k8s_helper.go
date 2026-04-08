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
// Delegates to the controller's logic via v1.Cluster.Key() to ensure consistency.
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
// Returns false only for NotFound errors; other errors cause a test failure.
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

// WaitPodsReady waits until all pods in the namespace are Ready or timeout.
func (h *K8sHelper) WaitPodsReady(ctx context.Context, namespace string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		pods, err := h.ListPods(ctx, namespace, "")
		if err != nil || len(pods) == 0 {
			time.Sleep(5 * time.Second)

			continue
		}

		allReady := true

		for _, pod := range pods {
			if pod.Status.Phase == corev1.PodSucceeded {
				continue // completed jobs are fine
			}

			if pod.Status.Phase != corev1.PodRunning {
				allReady = false

				break
			}

			for _, c := range pod.Status.ContainerStatuses {
				if !c.Ready {
					allReady = false

					break
				}
			}

			if !allReady {
				break
			}
		}

		if allReady {
			return
		}

		time.Sleep(5 * time.Second)
	}

	// Final check — fail with details
	pods, _ := h.ListPods(ctx, namespace, "")
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodSucceeded {
			ExpectWithOffset(1, pod.Status.Phase).To(Equal(corev1.PodRunning),
				"pod %s is not ready (phase=%s)", pod.Name, pod.Status.Phase)
		}
	}
}

// GetPodImages returns a map of pod name → container image for all pods in a namespace.
func (h *K8sHelper) GetPodImages(ctx context.Context, namespace string) map[string][]string {
	pods, err := h.ListPods(ctx, namespace, "")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	result := make(map[string][]string)

	for _, pod := range pods {
		var images []string
		for _, c := range pod.Spec.Containers {
			images = append(images, c.Image)
		}

		for _, ic := range pod.Spec.InitContainers {
			images = append(images, ic.Image)
		}

		result[pod.Name] = images
	}

	return result
}

// GetService retrieves a specific service.
func (h *K8sHelper) GetService(ctx context.Context, namespace, name string) (*corev1.Service, error) {
	return h.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
}

// CreateNamespace creates a namespace if it does not exist.
func (h *K8sHelper) CreateNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}

	_, err := h.clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}

	return err
}

// DeleteNamespace deletes a namespace.
func (h *K8sHelper) DeleteNamespace(ctx context.Context, name string) error {
	err := h.clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}

	return err
}

// CreateDockerRegistrySecret creates a docker-registry type secret.
func (h *K8sHelper) CreateDockerRegistrySecret(ctx context.Context, namespace, name, server, username, password string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		StringData: map[string]string{
			".dockerconfigjson": `{"auths":{"` + server + `":{"username":"` + username + `","password":"` + password + `"}}}`,
		},
	}

	_, err := h.clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}

	return err
}
