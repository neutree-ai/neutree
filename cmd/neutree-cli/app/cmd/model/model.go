package model

import (
	"github.com/spf13/cobra"
)

// Common flag variables
var (
	serverURL string
	apiKey    string
	workspace string
	registry  string
	insecure  bool
)

func NewModelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Management commands for models",
		Long:  `These commands help you manage models in the registry`,
	}

	// Add global flags
	cmd.PersistentFlags().StringVar(&serverURL, "server-url", "", "Server URL")
	cmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key")
	cmd.PersistentFlags().StringVarP(&workspace, "workspace", "w", "default", "Workspace to use")
	cmd.PersistentFlags().StringVarP(&registry, "registry", "r", "default", "Registry to use")
	cmd.PersistentFlags().BoolVar(&insecure, "insecure", false, "Skip TLS verification")

	// Add subcommands
	cmd.AddCommand(NewListCmd())
	cmd.AddCommand(NewGetCmd())
	cmd.AddCommand(NewDeleteCmd())
	cmd.AddCommand(NewPushCmd())
	cmd.AddCommand(NewPullCmd())

	return cmd
}
