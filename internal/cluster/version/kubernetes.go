package version

import (
	"strings"

	"helm.sh/helm/v3/pkg/chartutil"
	kubeversion "k8s.io/apimachinery/pkg/version"

	"github.com/neutree-ai/neutree/internal/semver"
)

const DefaultKubeSchedulerVersion = "v1.32.13"

var KubeSchedulerVersionsByMinor = map[string]string{
	"1.26": "v1.26.15",
	"1.27": "v1.27.16",
	"1.28": "v1.28.15",
	"1.29": "v1.29.14",
	"1.30": "v1.30.14",
	"1.31": "v1.31.14",
	"1.32": "v1.32.13",
}

func KubeSchedulerVersion(serverVersion *kubeversion.Info) string {
	if schedulerVersion, ok := KubeSchedulerVersionsByMinor[KubernetesServerMajorMinor(serverVersion)]; ok {
		return schedulerVersion
	}

	if detectedVersion := KubernetesServerVersion(serverVersion); detectedVersion != "" {
		return detectedVersion
	}

	return DefaultKubeSchedulerVersion
}

func KubeVersionFromServerVersion(serverVersion *kubeversion.Info) chartutil.KubeVersion {
	version := KubernetesServerVersion(serverVersion)
	if version == "" {
		return KubeVersionFromBaseVersion(DefaultKubeSchedulerVersion)
	}

	return KubeVersionFromBaseVersion(version)
}

func KubeVersionFromBaseVersion(version string) chartutil.KubeVersion {
	majorMinor, err := semver.MajorMinor(version)
	if err != nil || majorMinor == "" {
		return chartutil.KubeVersion{
			Version: version,
			Major:   "1",
			Minor:   "32",
		}
	}

	parts := strings.SplitN(majorMinor, ".", 2)

	return chartutil.KubeVersion{
		Version: version,
		Major:   parts[0],
		Minor:   parts[1],
	}
}

func KubernetesServerVersion(serverVersion *kubeversion.Info) string {
	if serverVersion == nil {
		return ""
	}

	if version, err := semver.BaseVersion(serverVersion.GitVersion); err == nil && version != "" {
		return version
	}

	return kubeServerSemverFromMajorMinor(serverVersion.Major, serverVersion.Minor)
}

func KubernetesServerMajorMinor(serverVersion *kubeversion.Info) string {
	version := KubernetesServerVersion(serverVersion)
	if version == "" {
		return ""
	}

	majorMinor, err := semver.MajorMinor(version)
	if err != nil {
		return ""
	}

	return majorMinor
}

func kubeServerSemverFromMajorMinor(major, minor string) string {
	major = kubeVersionNumericPrefix(strings.TrimPrefix(major, "v"))
	minor = kubeVersionNumericPrefix(minor)

	if major == "" || minor == "" {
		return ""
	}

	return "v" + major + "." + minor + ".0"
}

func kubeVersionNumericPrefix(value string) string {
	for offset, character := range value {
		if character < '0' || character > '9' {
			return value[:offset]
		}
	}

	return value
}
