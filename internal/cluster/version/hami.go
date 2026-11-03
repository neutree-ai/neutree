package version

import (
	"fmt"
	"strings"

	"github.com/neutree-ai/neutree/internal/component"
	"github.com/neutree-ai/neutree/internal/semver"
)

const MinVirtualizationClusterVersion = "v1.1.0"

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

// HAMiVersionForCluster selects the HAMi release for a cluster. Clusters below
// HAMiV210UpgradeGate keep HAMi v2.9.0; clusters at or above it use HAMi v2.10.0.
func HAMiVersionForCluster(clusterVersion string) (string, error) {
	supported, err := SupportsVirtualizationClusterVersion(clusterVersion)
	if err != nil {
		return "", err
	}

	if !supported {
		return "", fmt.Errorf("spec.accelerator_virtualization requires cluster version >= %s", MinVirtualizationClusterVersion)
	}

	baseVersion, err := semver.BaseVersion(strings.TrimSpace(clusterVersion))
	if err != nil {
		return "", err
	}

	legacy, err := semver.LessThan(baseVersion, HAMiV210UpgradeGate)
	if err != nil {
		return "", err
	}

	if legacy {
		return component.HAMi, nil
	}

	return component.V130HAMI, nil
}
