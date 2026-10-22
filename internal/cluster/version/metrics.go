package version

import (
	"strings"

	"github.com/neutree-ai/neutree/internal/semver"
)

const (
	MinKubeStateMetricsClusterVersion       = "v1.1.0"
	MinManagedMetricsExporterClusterVersion = "v1.1.0"
)

func SupportsKubeStateMetrics(version string) (bool, error) {
	return supportsClusterVersionAtLeast(version, MinKubeStateMetricsClusterVersion)
}

func SupportsManagedMetricsExporters(version string) (bool, error) {
	return supportsClusterVersionAtLeast(version, MinManagedMetricsExporterClusterVersion)
}

func supportsClusterVersionAtLeast(version string, minVersion string) (bool, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return false, nil
	}

	baseVersion, err := semver.BaseVersion(version)
	if err != nil {
		return false, err
	}

	lessThanMinVersion, err := semver.LessThan(baseVersion, minVersion)
	if err != nil {
		return false, err
	}

	return !lessThanMinVersion, nil
}
