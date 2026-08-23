package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ValidateClusterVersionEligibility checks whether one exact Cluster version
// belongs to the compatibility policy declared by a ReleaseInfo.
func ValidateClusterVersionEligibility(info *v1.ReleaseInfo, clusterVersion string) error {
	if info == nil || info.Spec == nil {
		return fmt.Errorf("release info spec is required")
	}

	cluster, err := parseExactVPrefixedSemVer(clusterVersion)
	if err != nil {
		return fmt.Errorf("invalid cluster version %q: %w", clusterVersion, err)
	}

	defaultVersion, err := parseExactVPrefixedSemVer(info.Spec.DefaultClusterVersion)
	if err != nil {
		return fmt.Errorf("invalid default cluster version %q: %w", info.Spec.DefaultClusterVersion, err)
	}

	if cluster.GreaterThan(defaultVersion) {
		return fmt.Errorf("cluster version %q exceeds default cluster version %q", clusterVersion, info.Spec.DefaultClusterVersion)
	}

	minor, err := NormalizeClusterMinor(clusterVersion)
	if err != nil {
		return err
	}

	for _, compatible := range info.Spec.CompatibleClusterBaselines {
		if compatible == minor {
			return nil
		}
	}

	return fmt.Errorf("cluster version %q has incompatible baseline %q", clusterVersion, minor)
}
