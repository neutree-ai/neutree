package engine

import (
	"github.com/spf13/cobra"
)

var workspace string

func NewEngineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "engine",
		Short: "Management commands for engines",
		Long:  `These commands help you manage inference engines and their versions.`,
	}

	cmd.PersistentFlags().StringVarP(&workspace, "workspace", "w", "default", "Workspace to use")

	cmd.AddCommand(NewRemoveVersionCmd())

	return cmd
}
