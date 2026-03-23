package packageimport

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
)

// downloadPackage downloads a package archive from the given URL to destPath.
// It writes to a temporary file first, then renames for atomicity.
func downloadPackage(ctx context.Context, url string, destPath string) error {
	klog.Infof("Downloading package from %s", url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.Wrap(err, "failed to create HTTP request")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "failed to download package")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("failed to download package: HTTP %s", resp.Status)
	}

	// Write to a temp file in the same directory for atomic rename
	dir := filepath.Dir(destPath)

	tmpFile, err := os.CreateTemp(dir, "neutree-download-*.tmp")
	if err != nil {
		return errors.Wrap(err, "failed to create temporary file")
	}

	tmpPath := tmpFile.Name()
	closed := false

	defer func() {
		if !closed {
			tmpFile.Close()
		}

		os.Remove(tmpPath) //nolint:errcheck // cleanup best-effort
	}()

	if resp.ContentLength > 0 {
		klog.Infof("Package size: %s", formatBytes(resp.ContentLength))
	} else {
		klog.Info("Package size unknown, downloading...")
	}

	written, err := io.Copy(tmpFile, resp.Body)
	if err != nil {
		return errors.Wrap(err, "failed to write package data")
	}

	if err := tmpFile.Close(); err != nil {
		return errors.Wrap(err, "failed to close temporary file")
	}

	closed = true

	klog.Infof("Downloaded %s", formatBytes(written))

	// Atomic rename
	if err := os.Rename(tmpPath, destPath); err != nil {
		return errors.Wrap(err, "failed to move downloaded file to destination")
	}

	return nil
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}

	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
