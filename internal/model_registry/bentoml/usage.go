package bentoml

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
)

// Usage is what a BentoML store currently holds.
type Usage struct {
	// ModelCount is the number of distinct model names, matching what ListModels
	// reports rather than the number of versions.
	ModelCount int
	// StorageBytes is the summed size of every file under the store.
	StorageBytes int64
}

// CollectUsage walks the store at homePath and measures it.
//
// The byte total is a real traversal, not a sum of the size strings in each
// model.yaml: those are human-readable, frozen at push time, and computed over a
// file set that differs from what actually sits on disk. Walking is expensive on
// a networked store, so callers are expected to throttle it rather than run it
// on every reconcile.
func CollectUsage(homePath string) (*Usage, error) {
	root := filepath.Join(homePath, "models")

	stat, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			klog.Warningf("Model store %s not found, reporting empty usage", root)

			return &Usage{}, nil
		}

		return nil, errors.Wrap(err, "model store not found")
	}

	if !stat.IsDir() {
		return nil, errors.Errorf("%s is not a directory", root)
	}

	usage := &Usage{}
	names := make(map[string]struct{})

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		usage.StorageBytes += info.Size()

		if d.Name() != ModelYAMLFileName && d.Name() != ModelJSONFileName {
			return nil
		}

		// A descriptor sits at <root>/<name>/<version>/, so the first path
		// segment below the root is the model name.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		if name, _, found := cutPathSegment(rel); found {
			names[name] = struct{}{}
		}

		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	usage.ModelCount = len(names)

	return usage, nil
}

// cutPathSegment splits off the first path segment of rel.
func cutPathSegment(rel string) (head, tail string, found bool) {
	for i := 0; i < len(rel); i++ {
		if os.IsPathSeparator(rel[i]) {
			return rel[:i], rel[i+1:], true
		}
	}

	return rel, "", false
}
