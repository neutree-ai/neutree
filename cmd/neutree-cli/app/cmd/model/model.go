package model

import (
	"github.com/spf13/cobra"
)

// model-specific flag variables
var (
	workspace string
	registry  string
)

func NewModelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Management commands for models",
		Long:  `These commands help you manage models in the registry`,
	}

	// model-specific flags
	cmd.PersistentFlags().StringVarP(&workspace, "workspace", "w", "default", "Workspace to use")
	cmd.PersistentFlags().StringVarP(&registry, "registry", "r", "default", "Registry to use")

	// Add subcommands
	cmd.AddCommand(NewListCmd())
	cmd.AddCommand(NewGetCmd())
	cmd.AddCommand(NewDeleteCmd())
	cmd.AddCommand(NewPushCmd())
	cmd.AddCommand(NewPullCmd())

	return cmd
}
