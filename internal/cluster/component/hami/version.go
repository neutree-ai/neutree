package hami

import (
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clustervalidation "github.com/neutree-ai/neutree/internal/cluster/validation"
	"github.com/neutree-ai/neutree/internal/semver"
)

func VersionForCluster(clusterVersion string) (string, error) {
	if err := clustervalidation.ValidateAcceleratorVirtualizationClusterSupport(
		v1.KubernetesClusterType, clusterVersion); err != nil {
		return "", err
	}

	baseVersion, err := semver.BaseVersion(strings.TrimSpace(clusterVersion))
	if err != nil {
		return "", err
	}

	legacy, err := semver.LessThan(baseVersion, "v1.3.0")
	if err != nil {
		return "", err
	}

	if legacy {
		return LegacyVersion, nil
	}

	return Version, nil
}
