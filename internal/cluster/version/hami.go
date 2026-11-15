package version

import (
	"fmt"
	"strings"

	"github.com/neutree-ai/neutree/internal/component"
	"github.com/neutree-ai/neutree/internal/semver"
)

const MinVirtualizationClusterVersion = "v1.1.0"

const (
	HAMiImageRegistry     = "docker.io"
	HAMiImageRepository   = "projecthami/hami"
	HAMiP1ImageRepository = "neutree/hami"
	HAMiP1ImageTag        = "v2.10.0-p1"
)

type HAMiImageProfile struct {
	ExtenderRepository     string
	ExtenderTag            string
	DevicePluginRepository string
	DevicePluginTag        string
}

var hamiImageProfiles = map[string]HAMiImageProfile{
	component.HAMi: {
		ExtenderRepository:     HAMiImageRepository,
		ExtenderTag:            component.HAMi,
		DevicePluginRepository: HAMiImageRepository,
		DevicePluginTag:        component.HAMi,
	},
	// HAMi v2.10.0's scheduler extender uses the P1 build because upstream
	// PR #2900 fixes positional allocation slots for regular init containers
	// when node device types are model-specific (for example, NVIDIA-L20) but
	// Pod requests use the generic NVIDIA resource type. The device-plugin and
	// monitor keep the original v2.10.0 image because the fix is scheduler-side.
	component.V130HAMI: {
		ExtenderRepository:     HAMiP1ImageRepository,
		ExtenderTag:            HAMiP1ImageTag,
		DevicePluginRepository: HAMiImageRepository,
		DevicePluginTag:        component.V130HAMI,
	},
}

func HAMiImageProfileForVersion(hamiVersion string) HAMiImageProfile {
	if profile, ok := hamiImageProfiles[hamiVersion]; ok {
		return profile
	}

	return HAMiImageProfile{
		ExtenderRepository:     HAMiImageRepository,
		ExtenderTag:            hamiVersion,
		DevicePluginRepository: HAMiImageRepository,
		DevicePluginTag:        hamiVersion,
	}
}

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
