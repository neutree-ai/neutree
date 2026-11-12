package version

import (
	"fmt"
	"strings"

	"github.com/neutree-ai/neutree/internal/semver"
)

const RouterClientConfigVersionGate = "v1.2.1"

// SupportsRouterClientConfig reports whether a Router version accepts the
// native client configuration flags.
func SupportsRouterClientConfig(routerVersion string) (bool, error) {
	if strings.TrimSpace(routerVersion) != routerVersion {
		return false, fmt.Errorf("invalid Router version %q: surrounding whitespace is not allowed", routerVersion)
	}

	return semver.LessThan(RouterClientConfigVersionGate, routerVersion)
}
