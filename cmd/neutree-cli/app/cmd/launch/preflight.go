package launch

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

// NewNeutreeCorePreflightCmd checks existing Cluster versions before an
// operator upgrades Neutree Core. Installation never invokes it implicitly.
func NewNeutreeCorePreflightCmd() *cobra.Command {
	return NewNeutreeCorePreflightCmdWithBuilder(nil)
}

// NewNeutreeCorePreflightCmdWithBuilder creates a preflight command with an
// injectable embedded Catalog builder for focused tests and edition assembly.
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
		Short:         "Check Cluster version compatibility before upgrading Neutree Core",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			if builderFactory == nil {
				return fmt.Errorf("release profile builder factory is required")
			}

			builder := builderFactory()
			target, err := buildNeutreeCorePreflightTarget(getCLIAppVersion(), builder)
			if err != nil {
				return err
			}

			apiClient, err := global.NewClient()
			if err != nil {
				return err
			}
			if apiClient.Generic == nil {
				return fmt.Errorf("generic API client is unavailable")
			}

			clusters, err := apiClient.Generic.List("Cluster", "")
			if err != nil {
				return fmt.Errorf("list clusters for upgrade preflight: %w", err)
			}

			return runNeutreeCorePreflight(clusters, target, command.OutOrStdout())
		},
	}
}

func buildNeutreeCorePreflightTarget(cliVersion string, builder releaseprofile.Builder) (*v1.ReleaseInfo, error) {
	if builder == nil {
		return nil, fmt.Errorf("release profile builder is required")
	}

	var baseline string
	if releaseprofile.IsWorkflowShortCommitBuild(cliVersion) {
		baseline = builder.CurrentReleaseInfoBaseline()
	} else {
		if isDevelopmentCLIVersion(cliVersion) {
			return nil, fmt.Errorf("cannot derive release info from CLI version %q", cliVersion)
		}

		var err error
		baseline, err = normalizeControlPlaneRelease(cliVersion)
		if err != nil {
			return nil, fmt.Errorf("cannot derive release info from CLI version %q: %w", cliVersion, err)
		}
	}

	info, err := builder.BuildReleaseInfo(baseline)
	if err != nil {
		return nil, fmt.Errorf("build embedded release info: %w", err)
	}

	return info, nil
}

func runNeutreeCorePreflight(rawClusters []json.RawMessage, target *v1.ReleaseInfo, output io.Writer) error {
	if output == nil {
		return fmt.Errorf("preflight output is required")
	}

	incompatible := 0
	for _, rawCluster := range rawClusters {
		var cluster v1.Cluster
		if err := json.Unmarshal(rawCluster, &cluster); err != nil {
			return fmt.Errorf("decode cluster for upgrade preflight: %w", err)
		}

		version := effectivePreflightClusterVersion(&cluster)
		if err := releaseprofile.ValidateClusterVersionCompatibility(target, version); err != nil {
			incompatible++
			_, _ = fmt.Fprintf(output, "%s: %s (%v)\n", preflightClusterReference(&cluster), preflightVersionForOutput(version), err)
		}
	}

	if incompatible > 0 {
		return fmt.Errorf("%d incompatible Clusters prevent upgrading to %s", incompatible, target.GetName())
	}

	return nil
}

func effectivePreflightClusterVersion(cluster *v1.Cluster) string {
	if cluster != nil && cluster.Status != nil && cluster.Status.Version != "" {
		return cluster.Status.Version
	}
	if cluster != nil && cluster.Spec != nil {
		return cluster.Spec.Version
	}

	return ""
}

func preflightClusterReference(cluster *v1.Cluster) string {
	workspace, name := "default", "<unnamed>"
	if cluster != nil && cluster.Metadata != nil {
		if cluster.Metadata.Workspace != "" {
			workspace = cluster.Metadata.Workspace
		}
		if cluster.Metadata.Name != "" {
			name = cluster.Metadata.Name
		}
	}

	return workspace + "/" + name
}

func preflightVersionForOutput(version string) string {
	if version == "" {
		return "<empty>"
	}

	return version
}
