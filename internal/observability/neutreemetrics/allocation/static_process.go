package allocation

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

const defaultProcFSRoot = "/proc"

type ProcFSEnvReader struct {
	Root string
}

func (r ProcFSEnvReader) Env(pid int) (map[string]string, error) {
	root := r.Root
	if root == "" {
		root = defaultProcFSRoot
	}

	raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil, err
	}

	environment := map[string]string{}

	for _, item := range strings.Split(string(raw), "\x00") {
		if item == "" {
			continue
		}

		key, value, ok := strings.Cut(item, "=")

		if ok {
			environment[key] = value
		}
	}

	return environment, nil
}

// ProcessDescendantReader observes only generic process topology. Accelerator
// adapters join those PIDs with their own vendor process observations.
type ProcessDescendantReader interface {
	DescendantPIDs(ancestorPID int) ([]int, error)
}

// newProcessTree returns the topology source for one collection: a snapshot of
// the tree at root, or the per-call reader when the tree cannot be read - which
// fails the way a missing /proc always did, and which callers already tolerate.
//
// It is a constructor rather than an accessor on purpose. Building the snapshot
// is a read of every process on the node, so its lifetime has to be the
// collection: a value built once and kept would answer from the tree as it was
// when it was built.
func newProcessTree(root string) ProcessDescendantReader {
	reader, err := NewCachedProcessDescendantReader(root)
	if err == nil {
		return reader
	}

	klog.Warningf("Falling back to per-call process tree reads: cannot snapshot %s: %v", root, err)

	return ProcFSProcessTreeReader{Root: root}
}

type ProcFSProcessTreeReader struct {
	Root string
}

func (r ProcFSProcessTreeReader) DescendantPIDs(ancestorPID int) ([]int, error) {
	if ancestorPID <= 0 {
		return nil, nil
	}

	root := r.Root
	if root == "" {
		root = defaultProcFSRoot
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	pids := []int{ancestorPID}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == ancestorPID {
			continue
		}

		isDescendant, err := isDescendant(root, pid, ancestorPID)
		if err != nil || !isDescendant {
			continue
		}

		pids = append(pids, pid)
	}

	sort.Ints(pids)

	return pids, nil
}

// CachedProcessDescendantReader answers every descendant query from one
// snapshot of the process tree, taken when it was created.
//
// It exists because a collection asks about every actor on a node, and each
// answer used to cost a full enumeration of /proc plus a parent walk per
// process. One snapshot serves them all.
//
// A snapshot is a point-in-time view, so its lifetime belongs to the caller:
// build one per collection and drop it. The type is immutable once built, which
// is why there is no lazy build and no sync.Once - a shared instance would
// answer from a stale tree forever, and immutability makes that mistake obvious
// rather than silent.
type CachedProcessDescendantReader struct {
	childrenByPID map[int][]int
}

func NewCachedProcessDescendantReader(root string) (CachedProcessDescendantReader, error) {
	if root == "" {
		root = defaultProcFSRoot
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return CachedProcessDescendantReader{}, err
	}

	reader := CachedProcessDescendantReader{
		childrenByPID: make(map[int][]int, len(entries)),
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}

		parentPID, ok, err := processParentPID(root, pid)
		if err != nil || !ok {
			continue
		}

		reader.childrenByPID[parentPID] = append(reader.childrenByPID[parentPID], pid)
	}

	return reader, nil
}

// DescendantPIDs returns the ancestor followed by its descendants, sorted
// ascending, matching the shape ProcFSProcessTreeReader produces.
//
// PID 1 intentionally reports no descendants: ProcFSProcessTreeReader stops its
// parent walk at init and so can never match PID 1 as an ancestor. Preserving
// that keeps this a pure performance change.
func (r CachedProcessDescendantReader) DescendantPIDs(ancestorPID int) ([]int, error) {
	if ancestorPID <= 0 {
		return nil, nil
	}

	if ancestorPID == 1 {
		return []int{ancestorPID}, nil
	}

	pids := []int{ancestorPID}
	visited := map[int]struct{}{ancestorPID: {}}
	pending := []int{ancestorPID}

	// A walk, not a recursive descent: the parent links are read one process at
	// a time, so churn can in principle produce a cycle and this must terminate.
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]

		for _, child := range r.childrenByPID[current] {
			if _, seen := visited[child]; seen {
				continue
			}

			visited[child] = struct{}{}

			pids = append(pids, child)
			pending = append(pending, child)
		}
	}

	sort.Ints(pids)

	return pids, nil
}

func isDescendant(root string, pid, ancestorPID int) (bool, error) {
	if pid <= 0 || ancestorPID <= 0 {
		return false, nil
	}

	if pid == ancestorPID {
		return true, nil
	}

	seen := map[int]struct{}{}
	currentPID := pid

	for currentPID > 1 {
		if currentPID == ancestorPID {
			return true, nil
		}

		if _, ok := seen[currentPID]; ok {
			return false, nil
		}

		seen[currentPID] = struct{}{}

		parentPID, ok, err := processParentPID(root, currentPID)
		if err != nil || !ok {
			return false, err
		}

		currentPID = parentPID
	}

	return false, nil
}

func processParentPID(root string, pid int) (int, bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "status"))
	if os.IsNotExist(err) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, err
	}

	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || key != "PPid" {
			continue
		}

		parentPID, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, false, err
		}

		return parentPID, true, nil
	}

	return 0, false, nil
}
