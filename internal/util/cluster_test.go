package util

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
)

func makeKubeConfig(clusterConfig, userConfig string) string {
	return fmt.Sprintf(`
apiVersion: v1
kind: Config
clusters:
- cluster:
%s
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
%s
`, clusterConfig, userConfig)
}

func TestGetApiServerUrlFromDecodedKubeConfig(t *testing.T) {
	tests := []struct {
		name           string
		kubeconfigYAML string
		expectedURL    string
		expectError    bool
	}{
		{
			name:           "valid kubeconfig with https server",
			kubeconfigYAML: makeKubeConfig("    server: https://kubernetes.example.com:6443\n    insecure-skip-tls-verify: true", "    token: test-token"),
			expectedURL:    "https://kubernetes.example.com:6443",
		},
		{
			name:           "valid kubeconfig with http server",
			kubeconfigYAML: makeKubeConfig("    server: http://localhost:8080", "    token: local-token"),
			expectedURL:    "http://localhost:8080",
		},
		{
			name:           "invalid kubeconfig - empty string",
			kubeconfigYAML: "",
			expectError:    true,
		},
		{
			name:           "invalid kubeconfig - malformed YAML",
			kubeconfigYAML: "not: valid: yaml: content:",
			expectError:    true,
		},
		{
			name:           "invalid kubeconfig - missing server",
			kubeconfigYAML: makeKubeConfig("    insecure-skip-tls-verify: true", "    token: test-token"),
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, err := GetApiServerUrlFromDecodedKubeConfig(tt.kubeconfigYAML)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expectedURL, url)
		})
	}
}

func TestGetTransportFromDecodedKubeConfig(t *testing.T) {
	tests := []struct {
		name           string
		kubeconfigYAML string
		expectError    bool
	}{
		{
			name:           "valid kubeconfig with token auth",
			kubeconfigYAML: makeKubeConfig("    server: https://kubernetes.example.com:6443\n    insecure-skip-tls-verify: true", "    token: test-bearer-token-12345"),
		},
		{
			name:           "valid kubeconfig with basic auth",
			kubeconfigYAML: makeKubeConfig("    server: https://kubernetes.example.com:6443\n    insecure-skip-tls-verify: true", "    username: admin\n    password: secret"),
		},
		{
			name:           "valid kubeconfig without explicit auth",
			kubeconfigYAML: makeKubeConfig("    server: https://kubernetes.example.com:6443\n    insecure-skip-tls-verify: true", "    {}"),
		},
		{
			name:           "invalid kubeconfig - empty string",
			kubeconfigYAML: "",
			expectError:    true,
		},
		{
			name:           "invalid kubeconfig - malformed YAML",
			kubeconfigYAML: "invalid yaml content @#$%",
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport, err := GetTransportFromDecodedKubeConfig(tt.kubeconfigYAML)

			if tt.expectError {
				require.Error(t, err)
				require.Nil(t, transport)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, transport)
		})
	}
}

func TestKubernetesClientSchemeRegistersRBAC(t *testing.T) {
	kinds, _, err := scheme.ObjectKinds(&rbacv1.Role{})

	require.NoError(t, err)
	require.NotEmpty(t, kinds)
	require.Equal(t, "Role", kinds[0].Kind)
}
