package image

import (
	"github.com/spf13/cobra"
)

// image-specific flag variables
var (
	workspace string
	registry  string
)

func NewImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Management commands for container images",
		Long:  `These commands help you manage container images in a Neutree-managed image registry`,
	}

	cmd.PersistentFlags().StringVarP(&workspace, "workspace", "w", "default", "Workspace to use")
	cmd.PersistentFlags().StringVarP(&registry, "image-registry", "r", "default", "Image registry to use")

	cmd.AddCommand(NewPushCmd())

	return cmd
}
