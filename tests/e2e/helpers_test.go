package e2e

import (
	"os"
	"path/filepath"
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

func TestIsDockerHubRegistryURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "docker io", url: "docker.io", want: true},
		{name: "docker io with scheme", url: "https://docker.io", want: true},
		{name: "docker hub alias", url: "index.docker.io", want: true},
		{name: "private registry", url: "registry.example.com", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isDockerHubRegistryURL(tt.url))
		})
	}
}
