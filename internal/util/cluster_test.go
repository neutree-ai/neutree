package util

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
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

// TestGetApiServerUrlFromDecodedKubeConfig tests extracting API server URL from kubeconfig
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
			expectError:    false,
		},
		{
			name:           "valid kubeconfig with http server",
			kubeconfigYAML: makeKubeConfig("    server: http://localhost:8080", "    token: local-token"),
			expectedURL:    "http://localhost:8080",
			expectError:    false,
		},
		{
			name:           "invalid kubeconfig - empty string",
			kubeconfigYAML: "",
			expectedURL:    "",
			expectError:    true,
		},
		{
			name:           "invalid kubeconfig - malformed YAML",
			kubeconfigYAML: "not: valid: yaml: content:",
			expectedURL:    "",
			expectError:    true,
		},
		{
			name:           "invalid kubeconfig - missing server",
			kubeconfigYAML: makeKubeConfig("    insecure-skip-tls-verify: true", "    token: test-token"),
			expectedURL:    "",
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, err := GetApiServerUrlFromDecodedKubeConfig(tt.kubeconfigYAML)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedURL, url)
			}
		})
	}
}

// TestGetTransportFromDecodedKubeConfig tests creating authenticated transport from kubeconfig
func TestGetTransportFromDecodedKubeConfig(t *testing.T) {
	tests := []struct {
		name           string
		kubeconfigYAML string
		expectError    bool
	}{
		{
			name:           "valid kubeconfig with token auth",
			kubeconfigYAML: makeKubeConfig("    server: https://kubernetes.example.com:6443\n    insecure-skip-tls-verify: true", "    token: test-bearer-token-12345"),
			expectError:    false,
		},
		{
			name:           "valid kubeconfig with basic auth",
			kubeconfigYAML: makeKubeConfig("    server: https://kubernetes.example.com:6443\n    insecure-skip-tls-verify: true", "    username: admin\n    password: secret"),
			expectError:    false,
		},
		{
			name:           "valid kubeconfig without explicit auth",
			kubeconfigYAML: makeKubeConfig("    server: https://kubernetes.example.com:6443\n    insecure-skip-tls-verify: true", "    {}"),
			expectError:    false,
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
				assert.Error(t, err)
				assert.Nil(t, transport)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, transport)
			}
		})
	}
}
