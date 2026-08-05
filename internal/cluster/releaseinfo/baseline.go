package releaseinfo

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

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
	if isDevelopmentOrDirtyBuild(buildIdentity) {
		return highestPersistedReleaseInfoBaseline(infos)
	}

	baseline, _, err := NormalizeControlPlaneRelease(buildIdentity)
	return baseline, err
}

func isDevelopmentOrDirtyBuild(buildIdentity string) bool {
	for _, part := range strings.FieldsFunc(strings.ToLower(strings.TrimSpace(buildIdentity)), func(r rune) bool {
		return r == '.' || r == '-' || r == '+'
	}) {
		if part == "dev" || part == "dirty" {
			return true
		}
	}

	return false
}

func highestPersistedReleaseInfoBaseline(infos []v1.ReleaseInfo) (string, error) {
	var selectedName string
	var selectedVersion *semver.Version

	for index := range infos {
		name := infos[index].GetName()
		if !strings.HasPrefix(name, "v") {
			continue
		}

		version, err := semver.StrictNewVersion(strings.TrimPrefix(name, "v"))
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
