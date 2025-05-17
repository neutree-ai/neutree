package model

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/pkg/client"
)

func NewDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete [model_name:version]",
		Short: "Delete a model from the registry",
		Long:  `Delete a specific model from the registry`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modelTag := args[0]

			// Parse model tag
			modelName, version, err := client.ParseModelTag(modelTag)
			if err != nil {
				return err
			}

			// Create client
			c := client.NewClient(serverURL, client.WithAPIKey(apiKey))

			if !force {
				fmt.Printf("Are you sure you want to delete model %s? [y/N]: ", modelTag)
				var response string
				fmt.Scanln(&response)
				if response != "y" && response != "Y" {
					fmt.Println("Operation cancelled")
					return nil
				}
			}

			if err := c.Models.Delete(workspace, registry, modelName, version); err != nil {
				return fmt.Errorf("failed to delete model %s: %w", modelTag, err)
			}

			fmt.Printf("Model %s deleted successfully\n", modelTag)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force deletion without confirmation")

	return cmd
}
