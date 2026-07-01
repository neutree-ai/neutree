package validation

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/semver"
)

const StaticNodeClusterFlowVersionGate = "v1.0.1"

type ClusterVersionUpdateErrorReason string

const (
	ClusterVersionUpdateInvalidVersionReason       ClusterVersionUpdateErrorReason = "invalid_version"
	ClusterVersionUpdateUnsupportedDowngradeReason ClusterVersionUpdateErrorReason = "unsupported_static_flow_downgrade"
)

type ClusterVersionUpdateError struct {
	Reason  ClusterVersionUpdateErrorReason
	Message string
	Hint    string
	Err     error
}

func (e *ClusterVersionUpdateError) Error() string {
	return e.Message
}

func (e *ClusterVersionUpdateError) Unwrap() error {
	return e.Err
}

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
		return &ClusterVersionUpdateError{
			Reason:  ClusterVersionUpdateInvalidVersionReason,
			Message: "invalid current cluster version",
			Hint:    err.Error(),
			Err:     err,
		}
	}

	desiredUsesStaticFlow, err := UsesStaticNodeClusterFlow(clusterType, desiredVersion)
	if err != nil {
		return &ClusterVersionUpdateError{
			Reason:  ClusterVersionUpdateInvalidVersionReason,
			Message: "invalid desired cluster version",
			Hint:    err.Error(),
			Err:     err,
		}
	}

	if previousUsesStaticFlow && !desiredUsesStaticFlow {
		return &ClusterVersionUpdateError{
			Reason: ClusterVersionUpdateUnsupportedDowngradeReason,
			Message: fmt.Sprintf("cluster version downgrade from static flow to legacy flow is not supported for %s clusters",
				clusterType),
			Hint: fmt.Sprintf("Keep spec.version greater than %s or recreate the cluster if legacy flow is required",
				StaticNodeClusterFlowVersionGate),
		}
	}

	return nil
}
