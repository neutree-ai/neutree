package accelerator

import (
	"strings"

	"github.com/neutree-ai/neutree/internal/semver"
)

const MinVirtualizationClusterVersion = "v1.1.0"

// SupportsVirtualizationClusterVersion gates accelerator virtualization by the
// Neutree cluster package version, not by the Kubernetes server version.
func SupportsVirtualizationClusterVersion(version string) (bool, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return false, nil
	}

	baseVersion, err := semver.BaseVersion(version)
	if err != nil {
		return false, err
	}

	lessThanMinVersion, err := semver.LessThan(baseVersion, MinVirtualizationClusterVersion)
	if err != nil {
		return false, err
	}

	return !lessThanMinVersion, nil
}
