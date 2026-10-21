package version

import "github.com/neutree-ai/neutree/internal/semver"

func SupportsEngineIsolation(version string) (bool, error) {
	return semver.LessThan("v1.0.0", version)
}

func SupportsRayNodeIPAddress(version string) (bool, error) {
	return semver.LessThan("v1.2.0", version)
}
