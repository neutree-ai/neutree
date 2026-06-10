package launch

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/neutree-ai/neutree/internal/version"
)

const fallbackNeutreeCoreVersion = "v0.0.1"

var gitDescribeLocalBuildPattern = regexp.MustCompile(`-\d+-g[0-9a-fA-F]+(?:-dirty)?$`)

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

	neutreeCoreCompatibilityPolicyForCurrentRelease = neutreeCoreCompatibilityPolicy{
		cliMin:    "v1.1.0-0",
		cliMax:    "v1.2.0-0",
		targetMin: "v1.1.0-0",
		targetMax: "v1.2.0-0",
	}
)

func defaultNeutreeCoreVersion() string {
	cliVersion := getCLIAppVersion()
	if isDevelopmentCLIVersion(cliVersion) {
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

	cli := semver.MustParse(cliVersion)
	policy := neutreeCoreCompatibilityPolicyForCurrentRelease

	if versionInRange(cli, policy.cliMin, policy.cliMax) {
		if versionInRange(target, policy.targetMin, policy.targetMax) {
			return nil
		}

		return fmt.Errorf("target version %q is not compatible with CLI version %q; allowed target range is [%s, %s)",
			targetVersion, cliVersion, policy.targetMin, policy.targetMax)
	}

	return fmt.Errorf("CLI version %q has no configured neutree-core compatibility range", cliVersion)
}

func isDevelopmentCLIVersion(cliVersion string) bool {
	return !isReleaseCLIVersion(cliVersion)
}

func isReleaseCLIVersion(cliVersion string) bool {
	if cliVersion == "" || cliVersion == "dev" || cliVersion == "unknown" {
		return false
	}

	if hasLocalBuildSuffix(cliVersion) {
		return false
	}

	_, err := semver.NewVersion(cliVersion)
	if err != nil {
		return false
	}

	return true
}

func hasLocalBuildSuffix(cliVersion string) bool {
	return strings.HasSuffix(cliVersion, "-dirty") || gitDescribeLocalBuildPattern.MatchString(cliVersion)
}

func versionInRange(v *semver.Version, minVersion, maxVersion string) bool {
	min := semver.MustParse(minVersion)
	max := semver.MustParse(maxVersion)

	return !v.LessThan(min) && v.LessThan(max)
}
