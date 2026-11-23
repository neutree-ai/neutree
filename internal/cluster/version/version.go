package version

import (
	"fmt"

	mastermindssemver "github.com/Masterminds/semver/v3"

	"github.com/neutree-ai/neutree/internal/semver"
)

const (
	StaticNodeClusterFlowVersionGate    = "v1.0.1"
	MinimumSelectableClusterVersionGate = StaticNodeClusterFlowVersionGate

	// HAMiV210UpgradeGate is the first Neutree cluster version in the v1.3
	// component profile. The profile changes several component behaviors at
	// this boundary, including HAMi v2.10.0 and scheduler images through
	// Kubernetes 1.35; each selection function documents its own rule.
	HAMiV210UpgradeGate = "v1.3.0"
)

func IsStaticNodeClusterFlowVersion(version string) (bool, error) {
	useStaticNodeFlow, err := semver.LessThan(StaticNodeClusterFlowVersionGate, version)
	if err != nil {
		return false, fmt.Errorf("invalid cluster version %q: %w", version, err)
	}

	return useStaticNodeFlow, nil
}

func IsSelectableClusterVersion(version string) (bool, error) {
	return semver.LessThan(MinimumSelectableClusterVersionGate, version)
}

func ValidateDesiredClusterVersion(desired string) error {
	if _, err := mastermindssemver.NewVersion(desired); err != nil {
		return fmt.Errorf("invalid desired cluster version %q: %w", desired, err)
	}

	return nil
}

func ValidateClusterVersionUpgrade(currentVersion, desiredVersion string) error {
	if _, err := mastermindssemver.NewVersion(currentVersion); err != nil {
		return ValidateDesiredClusterVersion(desiredVersion)
	}

	desiredIsOlder, err := semver.LessThan(desiredVersion, currentVersion)
	if err != nil {
		return fmt.Errorf("invalid desired cluster version %q: %w", desiredVersion, err)
	}

	if desiredIsOlder {
		return fmt.Errorf(
			"cluster version downgrade is not supported: current version is %s, desired version is %s",
			currentVersion,
			desiredVersion,
		)
	}

	return nil
}
