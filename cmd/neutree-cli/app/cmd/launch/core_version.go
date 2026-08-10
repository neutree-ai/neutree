package launch

import (
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/neutree-ai/neutree/internal/version"
)

const fallbackNeutreeCoreVersion = "v0.0.1"

var gitDescribeLocalBuildPattern = regexp.MustCompile(`-\d+-g[0-9a-fA-F]+(?:-dirty)?$`)

var (
	getCLIAppVersion = func() string {
		return version.Get().AppVersion
	}
)

func defaultNeutreeCoreVersion() string {
	cliVersion := getCLIAppVersion()
	if isDevelopmentCLIVersion(cliVersion) {
		return fallbackNeutreeCoreVersion
	}

	return cliVersion
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
