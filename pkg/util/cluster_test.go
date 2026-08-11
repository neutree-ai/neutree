package util

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestGetClientFromCluster(t *testing.T) {
	cluster := newKubernetesCluster(base64.StdEncoding.EncodeToString([]byte(validKubeconfig)))

	ctrlClient, err := GetClientFromCluster(cluster)

	require.NoError(t, err)
	require.NotNil(t, ctrlClient)
	rayClusterGVK := schema.GroupVersionKind{
		Group:   "ray.io",
		Version: "v1",
		Kind:    "RayCluster",
	}
	require.False(t, ctrlClient.Scheme().Recognizes(rayClusterGVK), "scheme recognizes %s", rayClusterGVK)

	cases := []struct {
		name string
		gvk  schema.GroupVersionKind
	}{
		{name: "admission", gvk: admissionregistrationv1.SchemeGroupVersion.WithKind("MutatingWebhookConfiguration")},
		{name: "apps", gvk: appsv1.SchemeGroupVersion.WithKind("Deployment")},
		{name: "core", gvk: corev1.SchemeGroupVersion.WithKind("Pod")},
		{name: "RBAC", gvk: rbacv1.SchemeGroupVersion.WithKind("Role")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.True(t, ctrlClient.Scheme().Recognizes(tc.gvk), "scheme does not recognize %s", tc.gvk)
		})
	}
}

func TestRESTConfigFromClusterSetsClientRateLimits(t *testing.T) {
	cluster := newKubernetesCluster(base64.StdEncoding.EncodeToString([]byte(validKubeconfig)))

	restConfig, err := restConfigFromCluster(cluster)

	require.NoError(t, err)
	require.Equal(t, float32(10), restConfig.QPS)
	require.Equal(t, 20, restConfig.Burst)
}

func TestGetClientFromClusterRejectsInvalidConfig(t *testing.T) {
	cases := []struct {
		name        string
		cluster     *v1.Cluster
		expectedErr string
	}{
		{
			name:        "nil cluster",
			cluster:     nil,
			expectedErr: "failed to get kubeconfig from cluster: failed to parse kubernetes cluster config: kubernetes cluster config is empty",
		},
		{
			name:        "missing spec",
			cluster:     &v1.Cluster{},
			expectedErr: "failed to get kubeconfig from cluster: failed to parse kubernetes cluster config: kubernetes cluster config is empty",
		},
		{
			name: "missing cluster config",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{},
			},
			expectedErr: "failed to get kubeconfig from cluster: failed to parse kubernetes cluster config: kubernetes cluster config is empty",
		},
		{
			name: "missing kubernetes config",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{}},
			},
			expectedErr: "failed to get kubeconfig from cluster: failed to parse kubernetes cluster config: kubernetes cluster config is empty",
		},
		{
			name:        "empty kubeconfig",
			cluster:     newKubernetesCluster(""),
			expectedErr: "failed to get kubeconfig from cluster: kubeconfig is empty",
		},
		{
			name:        "invalid base64 kubeconfig",
			cluster:     newKubernetesCluster("not-base64"),
			expectedErr: "failed to get kubeconfig from cluster: failed to decode kubeconfig: illegal base64 data at input byte 3",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrlClient, err := GetClientFromCluster(tc.cluster)

			require.Nil(t, ctrlClient)
			require.EqualError(t, err, tc.expectedErr)
		})
	}
}

func newKubernetesCluster(kubeconfig string) *v1.Cluster {
	return &v1.Cluster{
		Spec: &v1.ClusterSpec{
			Config: &v1.ClusterConfig{
				KubernetesConfig: &v1.KubernetesClusterConfig{
					Kubeconfig: kubeconfig,
				},
			},
		},
	}
}

const validKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://kubernetes.example.com:6443
    insecure-skip-tls-verify: true
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: test-token
`
