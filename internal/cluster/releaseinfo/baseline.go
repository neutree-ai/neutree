package releaseinfo

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
	versionText := strings.TrimSpace(clusterVersion)
	if !strings.HasPrefix(versionText, "v") {
		return "", fmt.Errorf("cluster version %q must use v-prefixed semantic version", clusterVersion)
	}

	version, err := semver.StrictNewVersion(strings.TrimPrefix(versionText, "v"))
	if err != nil {
		return "", fmt.Errorf("invalid cluster version %q: %w", clusterVersion, err)
	}

	return fmt.Sprintf("v%d.%d", version.Major(), version.Minor()), nil
}

// ResolveCurrentControlPlaneBaseline selects the ReleaseInfo baseline for the
// running control plane. Development and dirty builds are intentionally bound
// to the newest persisted ReleaseInfo because they do not identify a released
// control-plane baseline.
func ResolveCurrentControlPlaneBaseline(buildIdentity string, infos []v1.ReleaseInfo) (string, error) {
	if IsDevelopmentOrDirtyBuild(buildIdentity) {
		return highestPersistedReleaseInfoBaseline(infos)
	}

	baseline, err := NormalizeControlPlaneRelease(buildIdentity)

	return baseline, err
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

		version, err := parseStableReleaseInfoBaseline(name)
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

func parseStableReleaseInfoBaseline(baseline string) (*semver.Version, error) {
	if !strings.HasPrefix(baseline, "v") {
		return nil, fmt.Errorf("must use v-prefixed semantic version")
	}

	version, err := semver.StrictNewVersion(strings.TrimPrefix(baseline, "v"))
	if err != nil {
		return nil, err
	}

	if version.Prerelease() != "" || version.Metadata() != "" {
		return nil, fmt.Errorf("must be a stable release info baseline")
	}

	stableBaseline := fmt.Sprintf("v%d.%d.%d", version.Major(), version.Minor(), version.Patch())
	if baseline != stableBaseline {
		return nil, fmt.Errorf("must be an exact stable release info baseline")
	}

	return version, nil
}
