package releaseprofile

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
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

// NormalizeControlPlaneRelease validates an exact ReleaseInfo identity.
// Prerelease identities deliberately remain distinct from the eventual stable
// release.
func NormalizeControlPlaneRelease(buildIdentity string) (string, error) {
	if _, err := parseExactVPrefixedSemVer(buildIdentity); err != nil {
		return "", fmt.Errorf("invalid control-plane release %q: %w", buildIdentity, err)
	}

	return buildIdentity, nil
}

// ResolveCurrentControlPlaneBaseline selects the record consumed by Core. A
// development, dirty, or workflow-short-commit build cannot name a released
// policy, so it consumes the highest persisted ReleaseInfo instead.
func ResolveCurrentControlPlaneBaseline(buildIdentity string, infos []v1.ReleaseInfo) (string, error) {
	if IsDevelopmentOrDirtyBuild(buildIdentity) {
		return highestPersistedReleaseInfoBaseline(infos)
	}

	return NormalizeControlPlaneRelease(buildIdentity)
}

// IsDevelopmentOrDirtyBuild reports whether a build must resolve a persisted
// baseline instead of using its own identity.
func IsDevelopmentOrDirtyBuild(buildIdentity string) bool {
	identity := strings.TrimSpace(buildIdentity)
	if IsWorkflowShortCommitBuild(identity) {
		return true
	}

	for _, part := range strings.FieldsFunc(strings.ToLower(identity), func(r rune) bool {
		return r == '.' || r == '-' || r == '+'
	}) {
		if part == "dev" || part == "dirty" {
			return true
		}
	}

	return false
}

// IsWorkflowShortCommitBuild reports whether the identity is the release
// workflow's short-commit build value.
func IsWorkflowShortCommitBuild(buildIdentity string) bool {
	return workflowShortCommitBuildPattern.MatchString(strings.TrimSpace(buildIdentity))
}

func highestPersistedReleaseInfoBaseline(infos []v1.ReleaseInfo) (string, error) {
	var selectedName string
	var selectedVersion *semver.Version

	for index := range infos {
		name := infos[index].GetName()
		version, err := parseExactVPrefixedSemVer(name)
		if err != nil {
			continue
		}

		if selectedVersion == nil || version.GreaterThan(selectedVersion) {
			selectedName = name
			selectedVersion = version
		}
	}

	if selectedVersion == nil {
		return "", fmt.Errorf("no valid persisted release info baseline for development or dirty control-plane build")
	}

	return selectedName, nil
}

func parseExactVPrefixedSemVer(value string) (*semver.Version, error) {
	if strings.TrimSpace(value) != value || !strings.HasPrefix(value, "v") {
		return nil, fmt.Errorf("must use v-prefixed semantic version")
	}

	return semver.StrictNewVersion(strings.TrimPrefix(value, "v"))
}
