package version

import (
	"fmt"
	"strings"

	"github.com/neutree-ai/neutree/internal/component"
	"github.com/neutree-ai/neutree/internal/semver"
)

const (
	MinVirtualizationClusterVersion = "v1.1.0"
	hamiV210MinClusterVersion       = "v1.3.0"
)

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

	legacy, err := semver.LessThan(baseVersion, hamiV210MinClusterVersion)
	if err != nil {
		return "", err
	}

	if legacy {
		return component.LegacyHAMi, nil
	}

	return component.HAMi, nil
}
