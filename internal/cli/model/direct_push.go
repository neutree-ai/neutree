package model

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry/bentoml"
)

func ValidateDirectPushTarget(registry *v1.ModelRegistry, localNFSPath string) error {
	if registry == nil || registry.Spec == nil {
		return fmt.Errorf("direct model push only supports bentoml registries backed by nfs://")
	}

	parsedURL, err := url.Parse(registry.Spec.Url)
	if err != nil {
		return fmt.Errorf("invalid model registry URL %q: %w", registry.Spec.Url, err)
	}

	if registry.Spec.Type != v1.BentoMLModelRegistryType || parsedURL.Scheme != "nfs" {
		return fmt.Errorf("direct model push only supports bentoml registries backed by nfs://")
	}

	info, err := os.Stat(localNFSPath)
	if err != nil {
		return fmt.Errorf("local NFS path %s is not available: %w", localNFSPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("local NFS path %s is not a directory", localNFSPath)
	}

	probe, err := os.CreateTemp(localNFSPath, ".neutree-write-check-*")
	if err != nil {
		return fmt.Errorf("local NFS path %s is not writable: %w", localNFSPath, err)
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return fmt.Errorf("failed to close write check file under %s: %w", localNFSPath, err)
	}
	if err := os.Remove(probePath); err != nil {
		return fmt.Errorf("failed to remove write check file under %s: %w", localNFSPath, err)
	}

	return nil
}

func ImportArchiveToLocalNFS(localNFSPath, archivePath, name, version string, progress io.Writer) error {
	archive, err := os.Open(filepath.Clean(archivePath))
	if err != nil {
		return fmt.Errorf("failed to open model archive: %w", err)
	}
	defer archive.Close()

	if err := bentoml.ImportModel(localNFSPath, archive, name, version, true, progress); err != nil {
		return fmt.Errorf("failed to import model archive to local NFS path: %w", err)
	}

	return nil
}
