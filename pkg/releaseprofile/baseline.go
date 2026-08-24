package releaseprofile

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var workflowShortCommitBuildPattern = regexp.MustCompile(`^[0-9a-f]{7}$`)

// NormalizeClusterMinor returns the supported minor line for a complete Cluster
// version. Cluster prereleases belong to the minor line of their final release.
func NormalizeClusterMinor(clusterVersion string) (string, error) {
	if strings.TrimSpace(clusterVersion) != clusterVersion || !strings.HasPrefix(clusterVersion, "v") {
		return "", fmt.Errorf("cluster version %q must use v-prefixed semantic version", clusterVersion)
	}

	version, err := parseExactVPrefixedSemVer(clusterVersion)
	if err != nil {
		return "", fmt.Errorf("invalid cluster version %q: %w", clusterVersion, err)
	}

	return fmt.Sprintf("v%d.%d", version.Major(), version.Minor()), nil
}

// NormalizeControlPlaneRelease validates and returns the exact ReleaseInfo
// identity for a complete control-plane build. Prerelease identities remain
// distinct from their eventual stable releases; development and dirty builds
// are resolved from persisted ReleaseInfos by ResolveCurrentControlPlaneBaseline.
func NormalizeControlPlaneRelease(buildIdentity string) (string, error) {
	if strings.TrimSpace(buildIdentity) != buildIdentity || !strings.HasPrefix(buildIdentity, "v") {
		return "", fmt.Errorf("control-plane release %q must use v-prefixed semantic version", buildIdentity)
	}

	if _, err := parseExactVPrefixedSemVer(buildIdentity); err != nil {
		return "", fmt.Errorf("invalid control-plane release %q: %w", buildIdentity, err)
	}

	return buildIdentity, nil
}

// ResolveCurrentControlPlaneBaseline selects the ReleaseInfo baseline for the
// running control plane. Development and dirty builds are intentionally bound
// to the newest persisted ReleaseInfo because they do not identify a released
// control-plane baseline.
func ResolveCurrentControlPlaneBaseline(buildIdentity string, infos []v1.ReleaseInfo) (string, error) {
	if IsDevelopmentOrDirtyBuild(buildIdentity) {
		return highestPersistedReleaseInfoBaseline(infos)
	}

	return NormalizeControlPlaneRelease(buildIdentity)
}

// IsDevelopmentOrDirtyBuild reports whether a build identity must resolve its
// baseline from persisted ReleaseInfo data rather than its own version string.
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

// IsWorkflowShortCommitBuild reports whether the identity is the exact short
// commit tag emitted by the release workflow.
func IsWorkflowShortCommitBuild(buildIdentity string) bool {
	return workflowShortCommitBuildPattern.MatchString(strings.TrimSpace(buildIdentity))
}

func highestPersistedReleaseInfoBaseline(infos []v1.ReleaseInfo) (string, error) {
	var selectedName string
	var selectedVersion *semver.Version

	for index := range infos {
		name := infos[index].GetName()

		version, err := parseExactReleaseInfoBaseline(name)
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

func parseExactReleaseInfoBaseline(baseline string) (*semver.Version, error) {
	return parseExactVPrefixedSemVer(baseline)
}

func parseExactVPrefixedSemVer(value string) (*semver.Version, error) {
	if strings.TrimSpace(value) != value || !strings.HasPrefix(value, "v") {
		return nil, fmt.Errorf("must use v-prefixed semantic version")
	}

	return semver.StrictNewVersion(strings.TrimPrefix(value, "v"))
}
