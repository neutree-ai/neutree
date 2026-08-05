package launch

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

type clusterLister interface {
	List(kind, workspace string) ([]json.RawMessage, error)
}

// NewNeutreeCorePreflightCmd checks the current clusters before a control-plane
// upgrade. Installation intentionally does not invoke this command implicitly.
func NewNeutreeCorePreflightCmd() *cobra.Command {
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

			target, err := buildReleasePreflightTarget(getCLIAppVersion())
			if err != nil {
				return err
			}

			return runReleasePreflight(apiClient.Generic, target, command.OutOrStdout())
		},
	}
}

func buildReleasePreflightTarget(cliVersion string) (*v1.ReleaseInfo, error) {
	baseline, err := releaseinfo.NormalizeControlPlaneRelease(cliVersion)
	if err != nil {
		return nil, fmt.Errorf("cannot derive release info from CLI version %q: %w", cliVersion, err)
	}

	info, err := releaseprofile.NewCommunityReleaseInfoBuilder().BuildReleaseInfo(baseline)
	if err != nil {
		return nil, err
	}

	return info, nil
}

func runReleasePreflight(lister clusterLister, target *v1.ReleaseInfo, output io.Writer) error {
	if lister == nil {
		return fmt.Errorf("cluster lister is required")
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

	incompatible := 0
	for _, rawCluster := range rawClusters {
		var cluster v1.Cluster
		if err := json.Unmarshal(rawCluster, &cluster); err != nil {
			return fmt.Errorf("decode cluster for upgrade preflight: %w", err)
		}

		version := effectiveClusterVersion(&cluster)
		minor, versionErr := releaseinfo.NormalizeClusterMinor(version)
		if versionErr == nil && compatible[minor] {
			continue
		}

		incompatible++
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
			_, _ = fmt.Fprintf(output, "%s/%s: %s (invalid Cluster version: %v)\n", workspace, name, version, versionErr)
			continue
		}
		_, _ = fmt.Fprintf(output, "%s/%s: %s is not compatible with target ReleaseInfo %s\n", workspace, name, version, target.GetName())
	}

	if incompatible > 0 {
		return fmt.Errorf("%d incompatible Clusters prevent upgrading to %s", incompatible, target.GetName())
	}

	return nil
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
