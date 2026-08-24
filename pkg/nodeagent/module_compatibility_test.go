package nodeagent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExternalModuleBuildsSamePathNodeAgentEntrypoint(t *testing.T) {
	fixtureDir := filepath.Join("testdata", "external-module")
	command := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "neutree-node-agent"), "./cmd/neutree-node-agent")
	command.Dir = fixtureDir
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "external fixture build failed:\n%s", output)
}

func TestExternalModuleUsesOnlyPublicNodeAgentAPIs(t *testing.T) {
	fixtureDir := filepath.Join("testdata", "external-module")
	err := filepath.WalkDir(fixtureDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".go" {
			return walkErr
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range []string{"/internal/", "enterprise_overlay", "node-agent-overlay"} {
			if strings.Contains(string(content), forbidden) {
				t.Fatalf("fixture source %s contains forbidden boundary %q", path, forbidden)
			}
		}

		return nil
	})
	require.NoError(t, err)
}
