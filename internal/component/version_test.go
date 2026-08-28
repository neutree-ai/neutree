package component

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNodeAgentReleaseTagAndOfflineImageLists(t *testing.T) {
	require.Equal(t, "v1.2.0-rc.1", NeutreeNodeAgent)

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	expectedImages := []string{
		"neutree/neutree-node-agent:" + LegacyNeutreeNodeAgent,
		"neutree/neutree-node-agent:" + NeutreeNodeAgent,
	}
	for _, imageList := range []string{
		"scripts/builder/image-lists/cluster/kubernetes/images.txt",
		"scripts/builder/image-lists/cluster/ssh/images.txt",
	} {
		contents, err := os.ReadFile(filepath.Join(repoRoot, imageList))
		require.NoError(t, err)

		for _, image := range expectedImages {
			require.Contains(t, string(contents), image)
		}
	}
}
