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
