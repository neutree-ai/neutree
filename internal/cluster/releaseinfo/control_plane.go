package releaseinfo

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// NormalizeControlPlaneRelease returns the stable ReleaseInfo baseline for a
// complete control-plane build identity. Prerelease and nightly builds share
// their final vX.Y.Z baseline; development and dirty builds are resolved from
// persisted ReleaseInfos by ResolveCurrentControlPlaneBaseline.
func NormalizeControlPlaneRelease(buildIdentity string) (string, error) {
	identity := strings.TrimSpace(buildIdentity)
	if !strings.HasPrefix(identity, "v") {
		return "", fmt.Errorf("control-plane release %q must use v-prefixed semantic version", buildIdentity)
	}

	version, err := semver.StrictNewVersion(strings.TrimPrefix(identity, "v"))
	if err != nil {
		return "", fmt.Errorf("invalid control-plane release %q: %w", buildIdentity, err)
	}

	return fmt.Sprintf("v%d.%d.%d", version.Major(), version.Minor(), version.Patch()), nil
}
