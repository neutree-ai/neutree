package model

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry/bentoml"
)

var getMountSource = defaultGetMountSource

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

	expectedSource := normalizeNFSSource(parsedURL.Host + parsedURL.Path)

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

	mountSource, mounted, err := getMountSource(localNFSPath)
	if err != nil {
		return fmt.Errorf("failed to inspect local NFS mount for %s: %w", localNFSPath, err)
	}

	if !mounted {
		return fmt.Errorf("local NFS path %s is not an NFS mount point", localNFSPath)
	}

	if normalizeNFSSource(mountSource) != expectedSource {
		return fmt.Errorf("local NFS path %s is not mounted from %s", localNFSPath, expectedSource)
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

func ReadImportedModelVersion(localNFSPath, name, version string) (*v1.ModelVersion, error) {
	meta, err := bentoml.GetModelDetail(localNFSPath, strings.ToLower(name), strings.ToLower(version))
	if err != nil {
		return nil, fmt.Errorf("failed to read imported model metadata: %w", err)
	}

	return &v1.ModelVersion{
		Name:         meta.Version,
		CreationTime: meta.CreationTime,
		Size:         meta.Size,
		Module:       meta.Module,
	}, nil
}

func defaultGetMountSource(localPath string) (string, bool, error) {
	absPath, err := filepath.Abs(localPath)
	if err != nil {
		return "", false, err
	}

	absPath = filepath.Clean(absPath)

	switch runtime.GOOS {
	case "linux":
		return getLinuxMountSource(absPath)
	case "darwin":
		return getDarwinMountSource(absPath)
	default:
		return "", false, fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}

func getLinuxMountSource(absPath string) (string, bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", false, err
	}

	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}

		left, right, found := strings.Cut(line, " - ")
		if !found {
			continue
		}

		leftFields := strings.Fields(left)
		rightFields := strings.Fields(right)

		if len(leftFields) < 5 || len(rightFields) < 2 {
			continue
		}

		fsType := rightFields[0]
		if fsType != "nfs" && fsType != "nfs4" {
			continue
		}

		mountPoint := filepath.Clean(unescapeMountInfoField(leftFields[4]))
		if mountPoint == absPath {
			return rightFields[1], true, nil
		}
	}

	return "", false, nil
}

func getDarwinMountSource(absPath string) (string, bool, error) {
	output, err := exec.Command("mount").Output()
	if err != nil {
		return "", false, err
	}

	for _, line := range strings.Split(string(output), "\n") {
		source, rest, found := strings.Cut(line, " on ")
		if !found {
			continue
		}

		mountPoint, attrs, found := strings.Cut(rest, " (")
		if !found || !strings.Contains(attrs, "nfs") {
			continue
		}

		if filepath.Clean(mountPoint) == absPath {
			return source, true, nil
		}
	}

	return "", false, nil
}

func unescapeMountInfoField(value string) string {
	replacer := strings.NewReplacer(
		`\\`, `\`,
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)

	return replacer.Replace(value)
}

func normalizeNFSSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return source
	}

	return strings.TrimRight(source, "/")
}
