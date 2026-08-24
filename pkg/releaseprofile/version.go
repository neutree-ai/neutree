package releaseprofile

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
)

var workflowShortCommitBuildPattern = regexp.MustCompile(`^[0-9a-f]{7}$`)

// NormalizeClusterMinor returns the minor line for one complete Cluster version.
func NormalizeClusterMinor(clusterVersion string) (string, error) {
	version, err := parseExactVPrefixedSemVer(clusterVersion)
	if err != nil {
		return "", fmt.Errorf("invalid cluster version %q: %w", clusterVersion, err)
	}

	return fmt.Sprintf("v%d.%d", version.Major(), version.Minor()), nil
}

// IsWorkflowShortCommitBuild reports whether the identity is the release
// workflow's short-commit build value.
func IsWorkflowShortCommitBuild(buildIdentity string) bool {
	return workflowShortCommitBuildPattern.MatchString(strings.TrimSpace(buildIdentity))
}

func parseExactVPrefixedSemVer(value string) (*semver.Version, error) {
	if strings.TrimSpace(value) != value || !strings.HasPrefix(value, "v") {
		return nil, fmt.Errorf("must use v-prefixed semantic version")
	}

	return semver.StrictNewVersion(strings.TrimPrefix(value, "v"))
}
