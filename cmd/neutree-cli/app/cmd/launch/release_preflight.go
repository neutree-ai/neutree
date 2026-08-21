package launch

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	clusterpkg "github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
	"github.com/neutree-ai/neutree/pkg/client"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

type clusterLister interface {
	List(kind, workspace string) ([]json.RawMessage, error)
}

type clusterProfileVersionLister interface {
	ListClusterProfileVersions() ([]client.ClusterProfileVersion, error)
}

// NewNeutreeCorePreflightCmd checks the current clusters before a control-plane
// upgrade. Installation intentionally does not invoke this command implicitly.
func NewNeutreeCorePreflightCmd() *cobra.Command {
	return NewNeutreeCorePreflightCmdWithReleaseInfoBuilder(releaseprofile.NewCommunityReleaseInfoBuilder())
}

// NewNeutreeCorePreflightCmdWithReleaseInfoBuilder creates a preflight command
// that uses the supplied edition-specific ReleaseInfo builder.
func NewNeutreeCorePreflightCmdWithReleaseInfoBuilder(releaseInfoBuilder releaseprofile.ReleaseInfoBuilder) *cobra.Command {
	if releaseInfoBuilder == nil {
		releaseInfoBuilder = releaseprofile.NewCommunityReleaseInfoBuilder()
	}

	return &cobra.Command{
		Use:           "preflight",
		Short:         "Check Cluster compatibility before upgrading Neutree Core",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			apiClient, err := global.NewClient()
			if err != nil {
				return err
			}
			if apiClient.Generic == nil {
				return fmt.Errorf("generic API client is unavailable")
			}
			if apiClient.Clusters == nil {
				return fmt.Errorf("cluster API client is unavailable")
			}

			target, err := buildReleasePreflightTargetWithBuilder(getCLIAppVersion(), releaseInfoBuilder)
			if err != nil {
				return err
			}

			return runReleasePreflight(apiClient.Generic, apiClient.Clusters, target, command.OutOrStdout())
		},
	}
}

func buildReleasePreflightTarget(cliVersion string) (*v1.ReleaseInfo, error) {
	return buildReleasePreflightTargetWithBuilder(cliVersion, releaseprofile.NewCommunityReleaseInfoBuilder())
}

func buildReleasePreflightTargetWithBuilder(cliVersion string, releaseInfoBuilder releaseprofile.ReleaseInfoBuilder) (*v1.ReleaseInfo, error) {
	if releaseInfoBuilder == nil {
		return nil, fmt.Errorf("release info builder is required")
	}

	baseline, err := releaseinfo.NormalizeControlPlaneRelease(cliVersion)
	if err != nil {
		baselineProvider, ok := releaseInfoBuilder.(releaseprofile.CurrentReleaseInfoBaselineProvider)
		if !releaseinfo.IsWorkflowShortCommitBuild(cliVersion) || !ok {
			return nil, fmt.Errorf("cannot derive release info from CLI version %q: %w", cliVersion, err)
		}

		baseline = baselineProvider.CurrentReleaseInfoBaseline()
	}

	info, err := releaseInfoBuilder.BuildReleaseInfo(baseline)
	if err != nil {
		return nil, err
	}

	return info, nil
}

func runReleasePreflight(
	lister clusterLister,
	profileLister clusterProfileVersionLister,
	target *v1.ReleaseInfo,
	output io.Writer,
) error {
	if lister == nil {
		return fmt.Errorf("cluster lister is required")
	}

	if profileLister == nil {
		return fmt.Errorf("cluster profile version lister is required")
	}

	if target == nil || target.Spec == nil {
		return fmt.Errorf("target release info is required")
	}

	compatible := compatibleClusterMinorSet(target.Spec.CompatibleClusterBaselines)
	if len(compatible) == 0 {
		return fmt.Errorf("target release info has no compatible cluster baselines")
	}

	rawClusters, err := lister.List("Cluster", "")
	if err != nil {
		return fmt.Errorf("list clusters for upgrade preflight: %w", err)
	}

	profileVersions, err := profileLister.ListClusterProfileVersions()
	if err != nil {
		return fmt.Errorf("list cluster profiles for upgrade preflight: %w", err)
	}

	profiles := make(map[string]struct{}, len(profileVersions))

	for _, profile := range profileVersions {
		if profile.Version == "" || !v1.IsSupportedClusterType(profile.ClusterType) {
			continue
		}

		profiles[clusterProfileIdentity(profile.Version, profile.ClusterType)] = struct{}{}
	}

	incompatible := 0

	for _, rawCluster := range rawClusters {
		var cluster v1.Cluster
		if err := json.Unmarshal(rawCluster, &cluster); err != nil {
			return fmt.Errorf("decode cluster for upgrade preflight: %w", err)
		}

		version := effectiveClusterVersion(&cluster)

		minor, versionErr := releaseinfo.NormalizeClusterMinor(version)
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

		profileAware, err := clusterpkg.IsClusterProfileAwareVersion(version)
		if err != nil {
			incompatible++
			_, _ = fmt.Fprintf(output, "%s/%s: %s (cannot determine ClusterProfile requirement: %v)\n", workspace, name, version, err)

			continue
		}

		if !profileAware {
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

		if _, found := profiles[clusterProfileIdentity(version, clusterType)]; !found {
			incompatible++
			_, _ = fmt.Fprintf(output, "%s/%s: %s/%s has no exact ClusterProfile\n", workspace, name, version, clusterType)

			continue
		}
	}

	if incompatible > 0 {
		return fmt.Errorf("%d incompatible Clusters prevent upgrading to %s", incompatible, target.GetName())
	}

	return nil
}

func clusterProfileIdentity(version, clusterType string) string {
	return version + "\x00" + clusterType
}

func compatibleClusterMinorSet(baselines []string) map[string]bool {
	compatible := make(map[string]bool, len(baselines))
	for _, baseline := range baselines {
		compatible[strings.TrimSpace(baseline)] = true
	}

	return compatible
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
