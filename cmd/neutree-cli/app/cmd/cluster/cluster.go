package cluster

import (
    "github.com/spf13/cobra"
)

// NewClusterCmd returns the top-level 'cluster' command for neutree-cli.
func NewClusterCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "cluster",
        Short: "Manage Neutree clusters",
    }

    cmd.AddCommand(NewClusterImportCmd())

    return cmd
}
