package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveCLIBinaryUsesEnvironmentOverride(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "neutree-cli")
	require.NoError(t, os.WriteFile(cliPath, []byte("#!/bin/sh\n"), 0o755))

	t.Setenv("E2E_CLI_BINARY", cliPath)

	resolved, cleanup, err := resolveCLIBinary()
	require.NoError(t, err)
	require.False(t, cleanup)
	require.Equal(t, cliPath, resolved)
}

func TestClusterTemplatesRenderExternalAcceleratorExporterMode(t *testing.T) {
	cases := []struct {
		name         string
		templatePath string
		data         map[string]any
	}{
		{
			name:         "kubernetes",
			templatePath: filepath.Join("testdata", "k8s-cluster.yaml"),
			data: map[string]any{
				"CLUSTER_NAME":                               "external-k8s",
				"CLUSTER_WORKSPACE":                          "default",
				"CLUSTER_IMAGE_REGISTRY":                     "registry.example.com/neutree",
				"CLUSTER_VERSION":                            "v1.1.0",
				"CLUSTER_KUBECONFIG":                         "Y29uZmln",
				"CLUSTER_ROUTER_REPLICAS":                    "1",
				"CLUSTER_ROUTER_CPU":                         "1",
				"CLUSTER_ROUTER_MEMORY":                      "2Gi",
				"CLUSTER_MODEL_CACHES":                       nil,
				"CLUSTER_ACCELERATOR_EXPORTER_MODE":          "external",
				"CLUSTER_ACCELERATOR_VIRTUALIZATION_ENABLED": false,
			},
		},
		{
			name:         "static",
			templatePath: filepath.Join("testdata", "ssh-cluster.yaml"),
			data: map[string]any{
				"CLUSTER_NAME":                      "external-static",
				"CLUSTER_WORKSPACE":                 "default",
				"CLUSTER_IMAGE_REGISTRY":            "registry.example.com/neutree",
				"CLUSTER_VERSION":                   "v1.1.0",
				"CLUSTER_ACCELERATOR_TYPE":          "",
				"CLUSTER_ACCELERATOR_EXPORTER_MODE": "external",
				"CLUSTER_MODEL_CACHES":              nil,
				"CLUSTER_SSH_HEAD_IP":               "10.0.0.1",
				"CLUSTER_SSH_USER":                  "root",
				"CLUSTER_SSH_PRIVATE_KEY":           "cHJpdmF0ZS1rZXk=",
				"CLUSTER_WORKER_IPS":                nil,
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rendered, err := renderTemplate(tt.templatePath, tt.data)
			require.NoError(t, err)
			require.Contains(t, rendered, "metrics:\n      accelerator_exporter:\n        mode: \"external\"")
			require.False(t, strings.Contains(rendered, "<no value>"))
		})
	}
}
