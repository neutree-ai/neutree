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

// writeProcStatusFile lays down one process entry of a fake /proc tree.
func writeProcStatusFile(t *testing.T, root string, pid, parentPID int) {
	t.Helper()

	directory := filepath.Join(root, strconv.Itoa(pid))
	require.NoError(t, os.MkdirAll(directory, 0o755))

	contents := "Name:\ttest\nPPid:\t" + strconv.Itoa(parentPID) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "status"), []byte(contents), 0o600))
}

func TestCachedProcessDescendantReaderAnswersQueriesAfterProcIsGone(t *testing.T) {
	root := t.TempDir()
	writeProcStatusFile(t, root, 100, 1)
	writeProcStatusFile(t, root, 200, 100)
	writeProcStatusFile(t, root, 300, 200)
	writeProcStatusFile(t, root, 400, 1)

	snapshot, err := NewCachedProcessDescendantReader(root)
	require.NoError(t, err)

	// The whole point of the snapshot is that one enumeration per request is
	// enough. Removing the tree proves queries never fall back to reading it.
	require.NoError(t, os.RemoveAll(root))

	cases := []struct {
		name     string
		ancestor int
		want     []int
	}{
		{name: "ancestor with a grandchild", ancestor: 100, want: []int{100, 200, 300}},
		{name: "ancestor with one child", ancestor: 200, want: []int{200, 300}},
		{name: "leaf process", ancestor: 300, want: []int{300}},
		{name: "sibling subtree", ancestor: 400, want: []int{400}},
		{name: "pid absent from the tree", ancestor: 999, want: []int{999}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			pids, err := snapshot.DescendantPIDs(testCase.ancestor)

			require.NoError(t, err)
			assert.Equal(t, testCase.want, pids)
		})
	}
}

// TestCachedProcessDescendantReaderAgreesWithProcFSProcessTreeReader pins the refactor to
// the behaviour it replaces: swapping the reader for a snapshot must not move a
// single PID, including the reader's existing treatment of PID 1, which reports
// no descendants because its parent walk stops at init.
func TestCachedProcessDescendantReaderAgreesWithProcFSProcessTreeReader(t *testing.T) {
	root := t.TempDir()
	writeProcStatusFile(t, root, 1, 0)
	writeProcStatusFile(t, root, 100, 1)
	writeProcStatusFile(t, root, 200, 100)
	writeProcStatusFile(t, root, 300, 200)
	writeProcStatusFile(t, root, 400, 1)
	writeProcStatusFile(t, root, 500, 400)
	writeProcStatusFile(t, root, 600, 9999) // parent not present in the tree

	snapshot, err := NewCachedProcessDescendantReader(root)
	require.NoError(t, err)

	reader := ProcFSProcessTreeReader{Root: root}

	for _, ancestor := range []int{1, 100, 200, 300, 400, 500, 600, 999} {
		want, err := reader.DescendantPIDs(ancestor)
		require.NoError(t, err)

		got, err := snapshot.DescendantPIDs(ancestor)
		require.NoError(t, err)

		assert.Equalf(t, want, got, "descendants of pid %d", ancestor)
	}
}
