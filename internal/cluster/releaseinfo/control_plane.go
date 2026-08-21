package releaseinfo

import (
	"fmt"
	"strings"
)

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
