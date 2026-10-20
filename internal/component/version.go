// Package component defines versions and compatibility rules for bundled
// infrastructure components shared by deployment and cluster renderers.
package component

import (
	"fmt"
	"strings"

	"helm.sh/helm/v3/pkg/chartutil"
	kubeversion "k8s.io/apimachinery/pkg/version"

	"github.com/neutree-ai/neutree/internal/semver"
)

// VictoriaMetrics image versions.
const (
	VictoriaMetrics        = "v1.115.0"
	VictoriaMetricsCluster = VictoriaMetrics + "-cluster"
)

// KubeStateMetrics image version.
const KubeStateMetrics = "v2.15.0"

// NodeExporter image version.
const NodeExporter = "v1.8.2"

// LegacyNeutreeNodeAgent is the last NodeAgent image that still uses the
// legacy CLI contract rendered for clusters up to v1.1.1.
const LegacyNeutreeNodeAgent = "v1.1.0-rc.1"

// NeutreeNodeAgentV120 is the NodeAgent image pinned for clusters from v1.1.2
// through v1.2.0. Those clusters predate version-aware image selection, so they
// keep the image they were released with.
const NeutreeNodeAgentV120 = "v1.2.0-rc.1"

// NeutreeNodeAgent is the NodeAgent image running on clusters newer than
// v1.2.0.
const NeutreeNodeAgent = "v1.2.1-rc.1"

// Grafana image version.
const Grafana = "11.5.3"

// Vector image version.
const Vector = "0.47.0-debian"

// Kong image version.
const Kong = "3.9"

const (
	LegacyHAMi                      = "v2.9.0"
	HAMi                            = "v2.10.0"
	MinVirtualizationClusterVersion = "v1.1.0"
	hamiV210MinClusterVersion       = "v1.3.0"
	DefaultKubeSchedulerVersion     = "v1.32.13"
)

var legacyKubeSchedulerVersionsByMinor = map[string]string{
	"1.26": "v1.26.15",
	"1.27": "v1.27.16",
	"1.28": "v1.28.15",
	"1.29": "v1.29.14",
	"1.30": "v1.30.14",
	"1.31": "v1.31.14",
	"1.32": "v1.32.13",
}

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

// nodeAgentTier is one rung of the NodeAgent version ladder.
type nodeAgentTier struct {
	// maxVersion is the last cluster version in the tier. Empty means the tier is
	// open ended and covers every newer version.
	maxVersion string
	// contract is the parameter surface the image in this tier accepts.
	contract NodeAgentContract
	image    string
}

// nodeAgentTiers is the ladder, oldest tier first. A cluster version picks the
// first tier it does not exceed, so the cluster version alone decides both the
// CLI contract and the image; an accelerator profile has no say.
//
// The last tier is open ended, so shipping a new image is a one line change to
// NeutreeNodeAgent in version.go and nothing here moves.
var nodeAgentTiers = []nodeAgentTier{
	{
		maxVersion: "v1.1.1",
		contract:   NodeAgentContractLegacy,
		image:      nodeAgentImage(LegacyNeutreeNodeAgent),
	},
	{
		maxVersion: "v1.2.0",
		contract:   NodeAgentContractProfile,
		image:      nodeAgentImage(NeutreeNodeAgentV120),
	},
	{
		contract: NodeAgentContractProfile,
		image:    nodeAgentImage(NeutreeNodeAgent),
	},
}

// SelectNodeAgent resolves the CLI contract and image for a cluster version.
func SelectNodeAgent(version string) (NodeAgentSelection, error) {
	tier, err := pickNodeAgentTier(version)
	if err != nil {
		return NodeAgentSelection{}, err
	}

	return NodeAgentSelection{
		Contract: tier.contract,
		Image:    tier.image,
	}, nil
}

func pickNodeAgentTier(version string) (nodeAgentTier, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		// A cluster without a version predates every tier.
		return nodeAgentTiers[0], nil
	}

	baseVersion, err := semver.BaseVersion(version)
	if err != nil {
		return nodeAgentTier{}, err
	}

	for _, tier := range nodeAgentTiers {
		if tier.maxVersion == "" {
			return tier, nil
		}

		exceedsTier, err := semver.LessThan(tier.maxVersion, baseVersion)
		if err != nil {
			return nodeAgentTier{}, err
		}

		if !exceedsTier {
			return tier, nil
		}
	}

	// nodeAgentTiers always ends with an open ended tier.
	return nodeAgentTiers[len(nodeAgentTiers)-1], nil
}

func nodeAgentImage(version string) string {
	return "neutree/neutree-node-agent:" + version
}

func SupportsVirtualizationClusterVersion(version string) (bool, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return false, nil
	}

	baseVersion, err := semver.BaseVersion(version)
	if err != nil {
		return false, err
	}

	lessThanMinVersion, err := semver.LessThan(baseVersion, MinVirtualizationClusterVersion)
	if err != nil {
		return false, err
	}

	return !lessThanMinVersion, nil
}

func HAMiVersionForCluster(clusterVersion string) (string, error) {
	supported, err := SupportsVirtualizationClusterVersion(clusterVersion)
	if err != nil {
		return "", err
	}

	if !supported {
		return "", fmt.Errorf("spec.accelerator_virtualization requires cluster version >= %s", MinVirtualizationClusterVersion)
	}

	baseVersion, err := semver.BaseVersion(strings.TrimSpace(clusterVersion))
	if err != nil {
		return "", err
	}

	legacy, err := semver.LessThan(baseVersion, hamiV210MinClusterVersion)
	if err != nil {
		return "", err
	}

	if legacy {
		return LegacyHAMi, nil
	}

	return HAMi, nil
}

func KubeSchedulerVersionForHAMi(hamiVersion string, serverVersion *kubeversion.Info) string {
	schedulerVersions := KubeSchedulerVersionsByMinor
	if hamiVersion == LegacyHAMi {
		schedulerVersions = legacyKubeSchedulerVersionsByMinor
	}

	if schedulerVersion, ok := schedulerVersions[KubernetesServerMajorMinor(serverVersion)]; ok {
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
