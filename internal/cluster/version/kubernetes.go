package version

import (
	"fmt"
	"strings"

	"helm.sh/helm/v3/pkg/chartutil"
	kubeversion "k8s.io/apimachinery/pkg/version"

	"github.com/neutree-ai/neutree/internal/semver"
)

// defaultKubeMinor is the Kubernetes minor whose pinned scheduler image is
// used when the server version cannot be detected. Deriving the default from
// the pin table keeps the two from drifting apart.
const defaultKubeMinor = "1.32"

var KubeSchedulerVersionsByMinor = map[string]string{
	"1.26": "v1.26.15",
	"1.27": "v1.27.16",
	"1.28": "v1.28.15",
	"1.29": "v1.29.14",
	"1.30": "v1.30.14",
	"1.31": "v1.31.14",
	"1.32": "v1.32.13",
	"1.33": "v1.33.13",
	"1.34": "v1.34.12",
	"1.35": "v1.35.9",
}

var DefaultKubeSchedulerVersion = KubeSchedulerVersionsByMinor[defaultKubeMinor]

// SchedulerVersionSource reports how a scheduler image version was chosen.
type SchedulerVersionSource string

const (
	// SchedulerVersionPinned means the Kubernetes minor has a pinned image.
	SchedulerVersionPinned SchedulerVersionSource = "pinned"
	// SchedulerVersionDetected means the detected server version is used as-is.
	SchedulerVersionDetected SchedulerVersionSource = "detected"
	// SchedulerVersionDefault means detection was unusable, so the default is used.
	SchedulerVersionDefault SchedulerVersionSource = "default"
)

// KubeSchedulerVersionForClusterVersion selects the kube-scheduler image version
// for a cluster. The bundled images depend on the Neutree cluster version:
//
//   - below v1.3.0: scheduler images for Kubernetes 1.26-1.32
//   - v1.3.0 and above: scheduler images for Kubernetes 1.26-1.35
//
// A Kubernetes minor inside the cluster's range uses its pinned image. Anything
// else keeps the detected server version, and an unusable detection falls back
// to DefaultKubeSchedulerVersion. The returned error reports a cluster version
// that could not be evaluated; the caller keeps the fallback version but should
// surface why it was used.
func KubeSchedulerVersionForClusterVersion(clusterVersion string, serverVersion *kubeversion.Info) (string, SchedulerVersionSource, error) {
	detectedVersion := KubernetesServerVersion(serverVersion)
	if detectedVersion == "" {
		// The server version could not be detected; the chart and scheduler
		// image fall back to the default pinned version.
		return DefaultKubeSchedulerVersion, SchedulerVersionDefault, nil
	}

	baseClusterVersion, err := semver.BaseVersion(strings.TrimSpace(clusterVersion))
	if err != nil {
		return detectedVersion, SchedulerVersionDetected, fmt.Errorf("parse cluster version %q: %w", clusterVersion, err)
	}

	legacyCluster, err := semver.LessThan(baseClusterVersion, HAMiV210UpgradeGate)
	if err != nil {
		return detectedVersion, SchedulerVersionDetected, fmt.Errorf("compare cluster version %q: %w", clusterVersion, err)
	}

	minorVersion := KubernetesServerMajorMinor(serverVersion)

	if legacyCluster {
		// Clusters below v1.3.0 only bundle scheduler images up to Kubernetes
		// 1.32. The pin table also holds the 1.33-1.35 entries that the v1.3.0
		// profile adds, so a newer minor must keep the detected version
		// instead of borrowing a pin those clusters cannot pull.
		aboveLegacyRange, err := semver.LessThan("v1.32.0", minorVersion)
		if err != nil || aboveLegacyRange {
			return detectedVersion, SchedulerVersionDetected, nil
		}
	}

	// Clusters on v1.3.0 or later may use any pinned minor in their range.
	// Minors outside it (older than 1.26 or newer than the profile's last
	// bundled minor) have no image, so the detected version is kept.
	if pinnedVersion, ok := KubeSchedulerVersionsByMinor[minorVersion]; ok {
		return pinnedVersion, SchedulerVersionPinned, nil
	}

	return detectedVersion, SchedulerVersionDetected, nil
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
	if err != nil {
		majorMinor = defaultKubeMinor
	}

	major, minor, _ := strings.Cut(majorMinor, ".")

	return chartutil.KubeVersion{
		Version: version,
		Major:   major,
		Minor:   minor,
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
