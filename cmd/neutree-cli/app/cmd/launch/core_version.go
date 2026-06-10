package launch

import (
	"fmt"

	"github.com/Masterminds/semver/v3"

	"github.com/neutree-ai/neutree/internal/version"
)

const fallbackNeutreeCoreVersion = "v0.0.1"

type neutreeCoreCompatibilityPolicy struct {
	cliMin    string
	cliMax    string
	targetMin string
	targetMax string
}

var (
	getCLIAppVersion = func() string {
		return version.Get().AppVersion
	}

	neutreeCoreCompatibilityPolicies = []neutreeCoreCompatibilityPolicy{
		{
			cliMin:    "v1.0.0-0",
			cliMax:    "v1.1.0-0",
			targetMin: "v1.0.0-0",
			targetMax: "v1.1.0-0",
		},
		{
			cliMin:    "v1.1.0-0",
			cliMax:    "v1.2.0-0",
			targetMin: "v1.1.0-0",
			targetMax: "v1.2.0-0",
		},
	}
)

func defaultNeutreeCoreVersion() string {
	cliVersion := getCLIAppVersion()
	if isDevelopmentCLIVersion(cliVersion) {
		return fallbackNeutreeCoreVersion
	}

	if _, err := semver.NewVersion(cliVersion); err != nil {
		return fallbackNeutreeCoreVersion
	}

	return cliVersion
}

func validateNeutreeCoreVersionCompatibility(cliVersion, targetVersion string) error {
	target, err := semver.NewVersion(targetVersion)
	if err != nil {
		return fmt.Errorf("invalid target version %q: %w", targetVersion, err)
	}

	if isDevelopmentCLIVersion(cliVersion) {
		return nil
	}

	cli, err := semver.NewVersion(cliVersion)
	if err != nil {
		return fmt.Errorf("invalid CLI version %q: %w", cliVersion, err)
	}

	for _, policy := range neutreeCoreCompatibilityPolicies {
		if !versionInRange(cli, policy.cliMin, policy.cliMax) {
			continue
		}

		if versionInRange(target, policy.targetMin, policy.targetMax) {
			return nil
		}

		return fmt.Errorf("target version %q is not compatible with CLI version %q; allowed target range is [%s, %s)",
			targetVersion, cliVersion, policy.targetMin, policy.targetMax)
	}

	return fmt.Errorf("CLI version %q has no configured neutree-core compatibility range", cliVersion)
}

func isDevelopmentCLIVersion(cliVersion string) bool {
	return cliVersion == "" || cliVersion == "dev" || cliVersion == "unknown"
}

func versionInRange(v *semver.Version, minVersion, maxVersion string) bool {
	min := semver.MustParse(minVersion)
	max := semver.MustParse(maxVersion)

	return !v.LessThan(min) && v.LessThan(max)
}
