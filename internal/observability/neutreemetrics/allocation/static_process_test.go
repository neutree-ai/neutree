package allocation

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// writeProcEnvironFile lays down the environment of one process in a fake /proc
// tree, NUL-separated the way the real one is.
func writeProcEnvironFile(t *testing.T, root string, pid int, environ ...string) {
	t.Helper()

	directory := filepath.Join(root, strconv.Itoa(pid))
	require.NoError(t, os.MkdirAll(directory, 0o755))

	contents := strings.Join(environ, "\x00")
	require.NoError(t, os.WriteFile(filepath.Join(directory, "environ"), []byte(contents), 0o600))
}

// TestCachedProcessDescendantReaderReplacesProcFSProcessTreeReader pins both
// things the swap has to hold: the snapshot must not move a single PID relative
// to the reader it replaces, and it must answer without reading the tree again -
// which is what makes one enumeration per request sufficient.
//
// The table runs against the original reader first, so an expectation that is
// simply wrong fails there instead of quietly becoming the new truth.
func TestCachedProcessDescendantReaderReplacesProcFSProcessTreeReader(t *testing.T) {
	root := t.TempDir()
	writeProcStatusFile(t, root, 1, 0)
	writeProcStatusFile(t, root, 100, 1)
	writeProcStatusFile(t, root, 200, 100)
	writeProcStatusFile(t, root, 300, 200)
	writeProcStatusFile(t, root, 400, 1)
	writeProcStatusFile(t, root, 500, 400)
	writeProcStatusFile(t, root, 600, 9999) // parent not present in the tree

	replacement, err := NewCachedProcessDescendantReader(root)
	require.NoError(t, err)

	original := ProcFSProcessTreeReader{Root: root}

	cases := []struct {
		name     string
		ancestor int
		want     []int
	}{
		// PID 1 reports nothing below it: the reader's parent walk stops at init
		// and can never match it.
		{name: "pid 1 reports no descendants", ancestor: 1, want: []int{1}},
		{name: "ancestor with a grandchild", ancestor: 100, want: []int{100, 200, 300}},
		{name: "ancestor with one child", ancestor: 200, want: []int{200, 300}},
		{name: "leaf process", ancestor: 300, want: []int{300}},
		{name: "ancestor with a child", ancestor: 400, want: []int{400, 500}},
		{name: "parent absent from the tree", ancestor: 9999, want: []int{600, 9999}},
		{name: "pid absent from the tree", ancestor: 999, want: []int{999}},
	}

	t.Run("matches the reader it replaces", func(t *testing.T) {
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				pids, err := original.DescendantPIDs(testCase.ancestor)

				require.NoError(t, err)
				assert.Equal(t, testCase.want, pids)
			})
		}
	})

	// Removing the tree proves queries never fall back to reading it: an
	// implementation that re-read would have nothing left to read.
	require.NoError(t, os.RemoveAll(root))

	t.Run("answers from the snapshot once the tree is gone", func(t *testing.T) {
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				pids, err := replacement.DescendantPIDs(testCase.ancestor)

				require.NoError(t, err)
				assert.Equal(t, testCase.want, pids)
			})
		}
	})
}

// A process whose status cannot be read - as opposed to one that has exited -
// leaves the snapshot unable to answer for the processes beneath it. Returning
// it anyway would under-report descendants with nothing said, so the build fails
// and the caller falls back to per-query reads, where each miss is reported.
func TestNewProcessTreeRejectsASnapshotItCannotReadFully(t *testing.T) {
	root := t.TempDir()
	writeProcStatusFile(t, root, 100, 1)

	// A directory where a process's status file belongs: the root reads fine,
	// this one entry does not.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "200", "status"), 0o755))

	_, err := NewCachedProcessDescendantReader(root)
	require.Error(t, err)

	_, ok := newProcessTree(root).(ProcFSProcessTreeReader)

	assert.True(t, ok, "an unreadable entry should fall back to the per-call reader")
}

func TestNewProcessTreeSelectsTheReaderForTheRoot(t *testing.T) {
	readable := t.TempDir()
	writeProcStatusFile(t, readable, 100, 1)

	unreadable := filepath.Join(readable, "gone")

	cases := []struct {
		name     string
		root     string
		wantType any
		wantRoot string
	}{
		{
			name:     "a readable root is answered from one snapshot",
			root:     readable,
			wantType: CachedProcessDescendantReader{},
		},
		{
			// Falling back is not a failure: the per-call reader misses the way a
			// missing /proc always did, and the caller already tolerates that.
			name:     "an unreadable root falls back to the per-call reader",
			root:     unreadable,
			wantType: ProcFSProcessTreeReader{},
			wantRoot: unreadable,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reader := newProcessTree(testCase.root)

			assert.IsType(t, testCase.wantType, reader)

			if testCase.wantRoot == "" {
				return
			}

			fallback, ok := reader.(ProcFSProcessTreeReader)

			require.True(t, ok)
			assert.Equal(t, testCase.wantRoot, fallback.Root)
		})
	}
}
