package launch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKongPluginChecksumsAreDeterministic(t *testing.T) {
	pluginsRoot := writeKongPluginTree(t)

	first, err := kongPluginChecksums(pluginsRoot)
	require.NoError(t, err)

	second, err := kongPluginChecksums(pluginsRoot)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestKongPluginChecksumsOnlyChangesModifiedPlugin(t *testing.T) {
	pluginsRoot := writeKongPluginTree(t)

	before, err := kongPluginChecksums(pluginsRoot)
	require.NoError(t, err)

	gatewayHandler := filepath.Join(pluginsRoot, "neutree-ai-gateway", "handler.lua")
	require.NoError(t, os.WriteFile(gatewayHandler, []byte("return { VERSION = 'changed' }\n"), 0o600))

	after, err := kongPluginChecksums(pluginsRoot)
	require.NoError(t, err)

	assert.NotEqual(t, before["neutree-ai-gateway"], after["neutree-ai-gateway"])
	assert.Equal(t, before["neutree-ai-statistics"], after["neutree-ai-statistics"])
	assert.Equal(t, before["neutree-ai-access"], after["neutree-ai-access"])
	assert.Equal(t, before["neutree-ai-quota"], after["neutree-ai-quota"])
}

func TestKongPluginChecksumsRejectsMissingPluginDirectory(t *testing.T) {
	pluginsRoot := writeKongPluginTree(t)
	require.NoError(t, os.RemoveAll(filepath.Join(pluginsRoot, "neutree-ai-quota")))

	_, err := kongPluginChecksums(pluginsRoot)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "neutree-ai-quota")
}

func TestKongPluginChecksumsRejectsPluginFile(t *testing.T) {
	pluginsRoot := writeKongPluginTree(t)
	pluginPath := filepath.Join(pluginsRoot, "neutree-ai-quota")
	require.NoError(t, os.RemoveAll(pluginPath))
	require.NoError(t, os.WriteFile(pluginPath, []byte("not a directory\n"), 0o600))

	_, err := kongPluginChecksums(pluginsRoot)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "neutree-ai-quota")
}

func writeKongPluginTree(t *testing.T) string {
	t.Helper()

	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	for _, plugin := range []string{
		"neutree-ai-gateway",
		"neutree-ai-statistics",
		"neutree-ai-access",
		"neutree-ai-quota",
	} {
		pluginDir := filepath.Join(pluginsRoot, plugin)
		require.NoError(t, os.MkdirAll(pluginDir, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(pluginDir, "handler.lua"),
			[]byte("return { VERSION = '1.0.0' }\n"),
			0o600,
		))
	}

	return pluginsRoot
}
