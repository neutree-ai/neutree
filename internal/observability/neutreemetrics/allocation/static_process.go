package allocation

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const defaultProcFSRoot = "/proc"

// ProcessEnvReader observes the raw environment of a local process.
type ProcessEnvReader interface {
	Env(pid int) (map[string]string, error)
}

type ProcessEnvReaderFunc func(pid int) (map[string]string, error)

func (f ProcessEnvReaderFunc) Env(pid int) (map[string]string, error) {
	return f(pid)
}

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

type ProcessDescendantReaderFunc func(ancestorPID int) ([]int, error)

func (f ProcessDescendantReaderFunc) DescendantPIDs(ancestorPID int) ([]int, error) {
	return f(ancestorPID)
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
