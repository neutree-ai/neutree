package operator

import (
	"github.com/Masterminds/semver/v3"

	"github.com/neutree-ai/neutree/pkg/accelerator"
)

func nativeNvidiaClientManagement(policies []accelerator.GPUOperatorClusterPolicy) bool {
	for _, policy := range policies {
		version := policy.Labels["app.kubernetes.io/version"]
		if native, known := nvidiaVersionHasNativeClients(version); known {
			return native
		}
	}

	return false
}

func nvidiaVersionHasNativeClients(value string) (native, known bool) {
	version, err := semver.NewVersion(value)
	if err != nil {
		return false, false
	}

	return !version.LessThan(semver.MustParse("26.7.0")), true
}
