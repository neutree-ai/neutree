package cluster

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/pkg/client"
)

type checkOptions struct {
	name          string
	workspace     string
	targetVersion string
}

type preflightClient interface {
	UpgradePreflight(workspace, name, targetVersion string) (*client.ClusterUpgradePreflight, error)
}

// NewClusterCmd creates Cluster-specific CLI commands.
func NewClusterCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "cluster",
		Short: "Inspect Cluster release compatibility",
	}
	command.AddCommand(newCheckCmd())

	return command
}

func newCheckCmd() *cobra.Command {
	opts := &checkOptions{}
	command := &cobra.Command{
		Use:           "check",
		Short:         "Check Cluster upgrade compatibility",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			apiClient, err := global.NewClient()
			if err != nil {
				return err
			}

			return runCheck(apiClient.Clusters, *opts, command.OutOrStdout())
		},
	}

	command.Flags().StringVar(&opts.name, "name", "", "Cluster name")
	command.Flags().StringVarP(&opts.workspace, "workspace", "w", "default", "Workspace name")
	command.Flags().StringVar(&opts.targetVersion, "target-version", "", "Optional target Cluster version")
	_ = command.MarkFlagRequired("name")

	return command
}

func runCheck(apiClient preflightClient, opts checkOptions, output io.Writer) error {
	if opts.name == "" {
		return fmt.Errorf("cluster name is required")
	}

	if opts.workspace == "" {
		return fmt.Errorf("workspace is required")
	}

	result, err := apiClient.UpgradePreflight(opts.workspace, opts.name, opts.targetVersion)
	if err != nil {
		return fmt.Errorf("upgrade preflight failed: %w", err)
	}

	if result == nil || !result.Allowed {
		return fmt.Errorf("upgrade preflight rejected cluster %s/%s", opts.workspace, opts.name)
	}

	return json.NewEncoder(output).Encode(result)
}
