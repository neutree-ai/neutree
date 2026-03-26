package semver

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
)

func LessThan(a, b string) (bool, error) {
	versionA, err := semver.NewVersion(a)
	if err != nil {
		return false, err
	}

	versionB, err := semver.NewVersion(b)
	if err != nil {
		return false, err
	}

	return versionA.LessThan(versionB), nil
}

// BaseVersion parses a semver version string and returns only the
// "vMajor.Minor.Patch" portion, stripping any prerelease or metadata suffix.
// For example, "v0.17.1-cu130" returns "v0.17.1".
// The "v" prefix is preserved if present in the input.
func BaseVersion(version string) (string, error) {
	if version == "" {
		return "", nil
	}

	v, err := semver.NewVersion(version)
	if err != nil {
		return "", fmt.Errorf("failed to parse version %q: %w", version, err)
	}

	prefix := ""
	if strings.HasPrefix(version, "v") {
		prefix = "v"
	}

	return fmt.Sprintf("%s%d.%d.%d", prefix, v.Major(), v.Minor(), v.Patch()), nil
}
