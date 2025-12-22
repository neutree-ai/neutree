package util

import (
	"context"
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// K8sClient interface abstracts Kubernetes operations for testing
type K8sClient interface {
	GetPod(ctx context.Context, cluster *v1.Cluster, namespace, name string) (*corev1.Pod, error)
	GetPodLogs(ctx context.Context, cluster *v1.Cluster, namespace, podName string, opts *corev1.PodLogOptions) (io.ReadCloser, error)
}

// DefaultK8sClient is the default implementation using real Kubernetes clientset
type DefaultK8sClient struct{}

// GetPod retrieves a pod from the cluster
func (c *DefaultK8sClient) GetPod(ctx context.Context, cluster *v1.Cluster, namespace, name string) (*corev1.Pod, error) {
	clientset, err := GetClientSetFromCluster(cluster)
	if err != nil {
		return nil, err
	}

	return clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
}

// GetPodLogs retrieves logs from a pod
func (c *DefaultK8sClient) GetPodLogs(ctx context.Context, cluster *v1.Cluster, namespace, podName string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	clientset, err := GetClientSetFromCluster(cluster)
	if err != nil {
		return nil, err
	}

	req := clientset.CoreV1().Pods(namespace).GetLogs(podName, opts)

	return req.Stream(ctx)
}
