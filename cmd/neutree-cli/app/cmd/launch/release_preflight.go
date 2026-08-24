package launch

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/cobra"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

type clusterLister interface {
	List(kind, workspace string) ([]json.RawMessage, error)
}

// NewNeutreeCorePreflightCmd checks the current clusters before a control-plane
// upgrade. Installation intentionally does not invoke this command implicitly.
func NewNeutreeCorePreflightCmd() *cobra.Command {
	return NewNeutreeCorePreflightCmdWithBuilder(nil)
}

// NewNeutreeCorePreflightCmdWithBuilder creates a preflight command backed by
// an embedded ReleaseInfo and exact ClusterProfile catalog. No server-side
// profile listing endpoint is required.
func NewNeutreeCorePreflightCmdWithBuilder(builder releaseprofile.Builder) *cobra.Command {
	return newNeutreeCorePreflightCmd(func() releaseprofile.Builder {
		if builder != nil {
			return builder
		}

		return releaseprofile.NewBuilder()
	})
}

func newNeutreeCorePreflightCmd(builderFactory func() releaseprofile.Builder) *cobra.Command {
	return &cobra.Command{
		Use:           "preflight",
		Short:         "Check Cluster compatibility before upgrading Neutree Core",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			if builderFactory == nil {
				return fmt.Errorf("release profile builder factory is required")
			}

			builder := builderFactory()
			if builder == nil {
				return fmt.Errorf("release profile builder is required")
			}

			apiClient, err := global.NewClient()
			if err != nil {
				return err
			}
			if apiClient.Generic == nil {
				return fmt.Errorf("generic API client is unavailable")
			}
			target, err := buildReleasePreflightTargetWithBuilder(getCLIAppVersion(), builder)
			if err != nil {
				return err
			}

			profiles, err := builder.BuildClusterProfiles(target.GetName())
			if err != nil {
				return fmt.Errorf("build embedded cluster profile catalog: %w", err)
			}

			return runReleasePreflight(apiClient.Generic, profiles, target, command.OutOrStdout())
		},
	}
}

func buildReleasePreflightTargetWithBuilder(cliVersion string, builder releaseprofile.Builder) (*v1.ReleaseInfo, error) {
	if builder == nil {
		return nil, fmt.Errorf("release profile builder is required")
	}

	baseline, err := releaseprofile.NormalizeControlPlaneRelease(cliVersion)
	if err != nil {
		if !releaseprofile.IsWorkflowShortCommitBuild(cliVersion) {
			return nil, fmt.Errorf("cannot derive release info from CLI version %q: %w", cliVersion, err)
		}

		baseline = builder.CurrentReleaseInfoBaseline()
	}

	info, err := builder.BuildReleaseInfo(baseline)
	if err != nil {
		return nil, err
	}

	return info, nil
}

func runReleasePreflight(
	lister clusterLister,
	profiles []*v1.ClusterProfile,
	target *v1.ReleaseInfo,
	output io.Writer,
) error {
	if lister == nil {
		return fmt.Errorf("cluster lister is required")
	}

	if target == nil || target.Spec == nil {
		return fmt.Errorf("target release info is required")
	}

	if target.GetName() == "" {
		return fmt.Errorf("target release info name is required")
	}

	defaultVersion, err := parsePreflightVersion(target.Spec.DefaultClusterVersion)
	if err != nil {
		return fmt.Errorf("target release info default cluster version: %w", err)
	}

	compatible := compatibleClusterMinorSet(target.Spec.CompatibleClusterBaselines)
	if len(compatible) == 0 {
		return fmt.Errorf("target release info has no compatible cluster baselines")
	}

	defaultMinor, err := releaseprofile.NormalizeClusterMinor(target.Spec.DefaultClusterVersion)
	if err != nil {
		return fmt.Errorf("target release info default cluster version: %w", err)
	}

	if !compatible[defaultMinor] {
		return fmt.Errorf("target release info default cluster version %q has incompatible baseline %q", target.Spec.DefaultClusterVersion, defaultMinor)
	}

	profileIndex, err := indexEmbeddedClusterProfiles(profiles)
	if err != nil {
		return err
	}

	rawClusters, err := lister.List("Cluster", "")
	if err != nil {
		return fmt.Errorf("list clusters for upgrade preflight: %w", err)
	}

	incompatible := 0

	for _, rawCluster := range rawClusters {
		var cluster v1.Cluster
		if err := json.Unmarshal(rawCluster, &cluster); err != nil {
			return fmt.Errorf("decode cluster for upgrade preflight: %w", err)
		}

		version := effectiveClusterVersion(&cluster)

		minor, versionErr := releaseprofile.NormalizeClusterMinor(version)
		name := cluster.GetName()

		workspace := ""
		if cluster.Metadata != nil {
			workspace = cluster.Metadata.Workspace
		}

		if workspace == "" {
			workspace = "default"
		}

		if version == "" {
			version = "<empty>"
		}

		if versionErr != nil {
			incompatible++
			_, _ = fmt.Fprintf(output, "%s/%s: %s (invalid Cluster version: %v)\n", workspace, name, version, versionErr)

			continue
		}

		if !compatible[minor] {
			incompatible++
			_, _ = fmt.Fprintf(output, "%s/%s: %s is not compatible with target ReleaseInfo %s\n", workspace, name, version, target.GetName())

			continue
		}

		parsedVersion, err := parsePreflightVersion(version)
		if err != nil {
			incompatible++
			_, _ = fmt.Fprintf(output, "%s/%s: %s (invalid Cluster version: %v)\n", workspace, name, version, err)

			continue
		}

		if parsedVersion.GreaterThan(defaultVersion) {
			incompatible++
			_, _ = fmt.Fprintf(output, "%s/%s: %s exceeds target default Cluster version %s\n", workspace, name, version, target.Spec.DefaultClusterVersion)

			continue
		}

		clusterType := ""
		if cluster.Spec != nil {
			clusterType = cluster.Spec.Type
		}

		if !v1.IsSupportedClusterType(clusterType) {
			incompatible++
			_, _ = fmt.Fprintf(output, "%s/%s: %s has unsupported cluster type %q for ClusterProfile lookup\n", workspace, name, version, clusterType)

			continue
		}

		profile, found := profileIndex[version]
		if !found {
			incompatible++
			_, _ = fmt.Fprintf(output, "%s/%s: %s/%s has no exact ClusterProfile\n", workspace, name, version, clusterType)

			continue
		}

		if _, found := profile.Spec.ComponentsFor(clusterType); !found {
			incompatible++
			_, _ = fmt.Fprintf(output, "%s/%s: %s/%s has no component matrix\n", workspace, name, version, clusterType)
		}
	}

	if incompatible > 0 {
		return fmt.Errorf("%d incompatible Clusters prevent upgrading to %s", incompatible, target.GetName())
	}

	return nil
}

type preflightProfileComponent struct {
	name string
	ref  v1.ImageRef
}

// indexEmbeddedClusterProfiles validates the catalog before any Cluster is
// evaluated. A partial or duplicate catalog would otherwise make preflight
// results depend on map iteration order or silently accept an incomplete
// runtime matrix.
func indexEmbeddedClusterProfiles(profiles []*v1.ClusterProfile) (map[string]*v1.ClusterProfile, error) {
	if len(profiles) == 0 {
		return nil, fmt.Errorf("embedded cluster profile catalog is empty")
	}

	index := make(map[string]*v1.ClusterProfile, len(profiles))

	for _, profile := range profiles {
		if profile == nil || profile.Spec == nil || profile.Metadata == nil {
			return nil, fmt.Errorf("embedded cluster profile must include metadata and spec")
		}

		if profile.APIVersion != "v1" {
			return nil, fmt.Errorf("embedded cluster profile %q api version must be v1", profile.GetName())
		}

		if profile.Kind != v1.ClusterProfileKind {
			return nil, fmt.Errorf("embedded cluster profile %q kind must be %s", profile.GetName(), v1.ClusterProfileKind)
		}

		if profile.Metadata.Workspace != "" {
			return nil, fmt.Errorf("embedded cluster profile %q metadata.workspace must be empty", profile.GetName())
		}

		name := profile.GetName()
		if name == "" {
			return nil, fmt.Errorf("embedded cluster profile name is required")
		}

		if _, err := parsePreflightVersion(name); err != nil {
			return nil, fmt.Errorf("embedded cluster profile %q: %w", name, err)
		}

		if _, found := index[name]; found {
			return nil, fmt.Errorf("embedded cluster profile %q is duplicated", name)
		}

		if len(profile.Spec.Components) != 2 {
			return nil, fmt.Errorf("embedded cluster profile %q must contain exactly %q and %q component matrices", name, v1.SSHClusterType, v1.KubernetesClusterType)
		}

		for clusterType, components := range profile.Spec.Components {
			if !v1.IsSupportedClusterType(clusterType) {
				return nil, fmt.Errorf("embedded cluster profile %q contains unsupported cluster type %q", name, clusterType)
			}

			for _, component := range requiredPreflightComponents(clusterType, components) {
				if strings.TrimSpace(component.ref.Image) == "" || strings.TrimSpace(component.ref.Tag) == "" {
					return nil, fmt.Errorf("embedded cluster profile %q has incomplete %s/%s image reference", name, clusterType, component.name)
				}
			}
		}

		if _, found := profile.Spec.Components[v1.SSHClusterType]; !found {
			return nil, fmt.Errorf("embedded cluster profile %q is missing %s component matrix", name, v1.SSHClusterType)
		}

		if _, found := profile.Spec.Components[v1.KubernetesClusterType]; !found {
			return nil, fmt.Errorf("embedded cluster profile %q is missing %s component matrix", name, v1.KubernetesClusterType)
		}

		index[name] = profile
	}

	return index, nil
}

func requiredPreflightComponents(clusterType string, components v1.ClusterProfileComponents) []preflightProfileComponent {
	switch clusterType {
	case v1.SSHClusterType:
		return []preflightProfileComponent{
			{name: "ray_runtime", ref: components.RayRuntime},
			{name: "node_agent", ref: components.NodeAgent},
			{name: "node_exporter", ref: components.NodeExporter},
			{name: "vmagent", ref: components.VMAgent},
		}
	case v1.KubernetesClusterType:
		return []preflightProfileComponent{
			{name: "kubernetes_runtime", ref: components.KubernetesRuntime},
			{name: "router", ref: components.Router},
			{name: "node_agent", ref: components.NodeAgent},
			{name: "node_exporter", ref: components.NodeExporter},
			{name: "vmagent", ref: components.VMAgent},
			{name: "kube_state_metrics", ref: components.KubeStateMetrics},
		}
	default:
		return nil
	}
}

func compatibleClusterMinorSet(baselines []string) map[string]bool {
	compatible := make(map[string]bool, len(baselines))
	for _, baseline := range baselines {
		compatible[strings.TrimSpace(baseline)] = true
	}

	return compatible
}

func parsePreflightVersion(version string) (*semver.Version, error) {
	if strings.TrimSpace(version) != version || !strings.HasPrefix(version, "v") {
		return nil, fmt.Errorf("version %q must use v-prefixed semantic version", version)
	}

	return semver.StrictNewVersion(strings.TrimPrefix(version, "v"))
}

func effectiveClusterVersion(cluster *v1.Cluster) string {
	if cluster != nil && cluster.Status != nil && strings.TrimSpace(cluster.Status.Version) != "" {
		return strings.TrimSpace(cluster.Status.Version)
	}

	if cluster != nil && cluster.Spec != nil {
		return strings.TrimSpace(cluster.Spec.Version)
	}

	return ""
}
