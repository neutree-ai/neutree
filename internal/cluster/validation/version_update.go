package validation

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/semver"
)

const StaticNodeClusterFlowVersionGate = "v1.0.1"

func UsesStaticNodeClusterFlow(clusterType, version string) (bool, error) {
	if clusterType != v1.SSHClusterType {
		return false, nil
	}

	useStaticNodeFlow, err := semver.LessThan(StaticNodeClusterFlowVersionGate, version)
	if err != nil {
		return false, fmt.Errorf("invalid cluster version %q: %w", version, err)
	}

	return useStaticNodeFlow, nil
}

func ValidateStaticNodeClusterFlowVersionUpdate(clusterType, previousVersion, desiredVersion string) error {
	previousUsesStaticFlow, err := UsesStaticNodeClusterFlow(clusterType, previousVersion)
	if err != nil {
		return fmt.Errorf("invalid current cluster version: %w", err)
	}

	desiredUsesStaticFlow, err := UsesStaticNodeClusterFlow(clusterType, desiredVersion)
	if err != nil {
		return fmt.Errorf("invalid desired cluster version: %w", err)
	}

	if previousUsesStaticFlow && !desiredUsesStaticFlow {
		return fmt.Errorf(
			"cluster version downgrade from static flow to legacy flow is not supported for %s clusters; "+
				"keep spec.version greater than %s or recreate the cluster if legacy flow is required",
			clusterType,
			StaticNodeClusterFlowVersionGate,
		)
	}

	return nil
}
