package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"time"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

// K8sHelper provides Kubernetes client operations for e2e tests.
type K8sHelper struct {
	clientset  *kubernetes.Clientset
	restConfig *rest.Config
}

// NewK8sHelper creates a K8sHelper from a base64-encoded kubeconfig.
func NewK8sHelper(kubeconfigBase64 string) *K8sHelper {
	kubeconfigBytes, err := base64.StdEncoding.DecodeString(kubeconfigBase64)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to decode kubeconfig base64")

	rc, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create REST config from kubeconfig")

	clientset, err := kubernetes.NewForConfig(rc)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create kubernetes clientset")

	return &K8sHelper{clientset: clientset, restConfig: rc}
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

// GetService retrieves a specific service.
func (h *K8sHelper) GetService(ctx context.Context, namespace, name string) (*corev1.Service, error) {
	return h.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
}

// GetSecret retrieves a specific secret.
func (h *K8sHelper) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	return h.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
}

// GetConfigMap retrieves a specific configmap.
func (h *K8sHelper) GetConfigMap(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error) {
	return h.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
}

// GetPVC retrieves a specific persistent volume claim.
func (h *K8sHelper) GetPVC(ctx context.Context, namespace, name string) (*corev1.PersistentVolumeClaim, error) {
	return h.clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
}

// GetServiceAccount retrieves a specific service account.
func (h *K8sHelper) GetServiceAccount(ctx context.Context, namespace, name string) (*corev1.ServiceAccount, error) {
	return h.clientset.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
}

// GetRole retrieves a specific role.
func (h *K8sHelper) GetRole(ctx context.Context, namespace, name string) (*rbacv1.Role, error) {
	return h.clientset.RbacV1().Roles(namespace).Get(ctx, name, metav1.GetOptions{})
}

// GetRoleBinding retrieves a specific role binding.
func (h *K8sHelper) GetRoleBinding(ctx context.Context, namespace, name string) (*rbacv1.RoleBinding, error) {
	return h.clientset.RbacV1().RoleBindings(namespace).Get(ctx, name, metav1.GetOptions{})
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

	rc, err := clientcmd.RESTConfigFromKubeConfig(data)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create REST config from kubeconfig")

	clientset, err := kubernetes.NewForConfig(rc)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to create kubernetes clientset")

	return &K8sHelper{clientset: clientset, restConfig: rc}
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

// GetPodImages returns a map of pod name -> container image for all pods in a namespace.
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

// GetStatefulSet retrieves a specific statefulset.
func (h *K8sHelper) GetStatefulSet(ctx context.Context, namespace, name string) (*appsv1.StatefulSet, error) {
	return h.clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
}

// GetJob retrieves a specific job.
func (h *K8sHelper) GetJob(ctx context.Context, namespace, name string) (*batchv1.Job, error) {
	return h.clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
}

// ListStatefulSets lists statefulsets in a namespace with optional label selector.
func (h *K8sHelper) ListStatefulSets(ctx context.Context, namespace, labelSelector string) ([]appsv1.StatefulSet, error) {
	list, err := h.clientset.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}

	return list.Items, nil
}

// ListJobs lists jobs in a namespace with optional label selector.
func (h *K8sHelper) ListJobs(ctx context.Context, namespace, labelSelector string) ([]batchv1.Job, error) {
	list, err := h.clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}

	return list.Items, nil
}

// ServiceProxyGet sends a GET request to a service via the K8s API server proxy.
// Path format: /api/v1/namespaces/{ns}/services/{svc}:{port}/proxy/{path}
func (h *K8sHelper) ServiceProxyGet(ctx context.Context, namespace, svcName, port, path string) error {
	result := h.clientset.CoreV1().Services(namespace).ProxyGet("http", svcName, port, path, nil)
	_, err := result.DoRaw(ctx)

	return err
}

// CreateDockerRegistrySecret creates a docker-registry type secret.
func (h *K8sHelper) CreateDockerRegistrySecret(ctx context.Context, namespace, name, server, username, password string) error {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	dockerConfig := map[string]any{
		"auths": map[string]any{
			server: map[string]string{
				"username": username,
				"password": password,
				"auth":     auth,
			},
		},
	}

	configJSON, err := json.Marshal(dockerConfig)
	if err != nil {
		return err
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		StringData: map[string]string{
			".dockerconfigjson": string(configJSON),
		},
	}

	_, err = h.clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}

	return err
}
