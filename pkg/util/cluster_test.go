package util

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestGetClientFromClusterBuildsClientFromKubeconfig(t *testing.T) {
	cluster := &v1.Cluster{
		Spec: &v1.ClusterSpec{
			Config: &v1.ClusterConfig{
				KubernetesConfig: &v1.KubernetesClusterConfig{
					Kubeconfig: base64.StdEncoding.EncodeToString([]byte(testKubeconfig)),
				},
			},
		},
	}

	ctrlClient, err := GetClientFromCluster(cluster)

	require.NoError(t, err)
	require.NotNil(t, ctrlClient)
}

func TestGetClientFromClusterRejectsMissingKubeconfig(t *testing.T) {
	cluster := &v1.Cluster{Spec: &v1.ClusterSpec{Config: &v1.ClusterConfig{
		KubernetesConfig: &v1.KubernetesClusterConfig{},
	}}}

	ctrlClient, err := GetClientFromCluster(cluster)

	require.Error(t, err)
	require.Nil(t, ctrlClient)
}

func TestKubernetesSchemeRegistersRBAC(t *testing.T) {
	kinds, _, err := kubernetesScheme.ObjectKinds(&rbacv1.Role{})

	require.NoError(t, err)
	require.NotEmpty(t, kinds)
	require.Equal(t, "Role", kinds[0].Kind)
}

const testKubeconfig = `
apiVersion: v1
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
