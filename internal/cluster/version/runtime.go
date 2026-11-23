package version

import "github.com/neutree-ai/neutree/internal/semver"

const (
	// engineIsolationVersionGate is the highest cluster version that mounts NFS
	// inside ray_container and needs elevated privileges. Greater versions use
	// DOOD engine isolation.
	engineIsolationVersionGate = "v1.0.0"

	// rayNodeIPAddressVersionGate is the highest cluster version that keeps the
	// legacy Ray startup arguments. Greater versions pass the node IP address.
	rayNodeIPAddressVersionGate = "v1.2.0"
)

func SupportsEngineIsolation(version string) (bool, error) {
	return semver.LessThan(engineIsolationVersionGate, version)
}

func SupportsRayNodeIPAddress(version string) (bool, error) {
	return semver.LessThan(rayNodeIPAddressVersionGate, version)
}
