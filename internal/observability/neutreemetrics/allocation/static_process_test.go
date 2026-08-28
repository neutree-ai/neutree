package allocation

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcFSEnvReaderReadsProcessEnvironment(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "1234")
	require.NoError(t, os.MkdirAll(directory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "environ"), []byte("FOO=bar\x00EMPTY=\x00ignored"), 0o600))

	environment, err := (ProcFSEnvReader{Root: root}).Env(1234)

	require.NoError(t, err)
	assert.Equal(t, "bar", environment["FOO"])
	assert.Equal(t, "", environment["EMPTY"])
}

func TestProcFSProcessTreeReaderFindsDescendantPIDs(t *testing.T) {
	root := t.TempDir()
	writeProcStatus := func(pid, parentPID int) {
		directory := filepath.Join(root, strconv.Itoa(pid))
		require.NoError(t, os.MkdirAll(directory, 0o755))
		contents := "Name:\ttest\nPPid:\t" + strconv.Itoa(parentPID) + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(directory, "status"), []byte(contents), 0o600))
	}

	writeProcStatus(100, 1)
	writeProcStatus(200, 100)
	writeProcStatus(300, 200)
	writeProcStatus(400, 1)

	pids, err := (ProcFSProcessTreeReader{Root: root}).DescendantPIDs(100)

	require.NoError(t, err)
	assert.Equal(t, []int{100, 200, 300}, pids)
}

func TestProcFSProcessTreeReaderRejectsInvalidAncestor(t *testing.T) {
	pids, err := (ProcFSProcessTreeReader{}).DescendantPIDs(0)

	require.NoError(t, err)
	assert.Nil(t, pids)
}
