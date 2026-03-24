package packageimport

import (
	"context"
	"fmt"
	"net/http"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
)

// streamExtractPackage downloads a tar.gz package from url and extracts it
// directly to destPath without writing the archive to disk first.
func streamExtractPackage(ctx context.Context, url string, extractor *Extractor, destPath string) error {
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

	if resp.ContentLength > 0 {
		klog.Infof("Package size: %s", formatBytes(resp.ContentLength))
	}

	return extractor.ExtractFromReader(resp.Body, destPath)
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
