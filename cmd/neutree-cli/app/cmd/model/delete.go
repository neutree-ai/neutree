package model

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
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

			clientOptions := []client.ClientOption{
				client.WithAPIKey(global.APIKey),
			}

			if global.Insecure {
				clientOptions = append(clientOptions, client.WithInsecureSkipVerify())
			}

			// Create client
			c := client.NewClient(global.ServerURL, clientOptions...)
			_, err = c.ModelRegistries.Get(workspace, registry) // Ensure registry exists
			if err != nil {
				return fmt.Errorf("failed to get model registry %s: %w", registry, err)
			}

			if !force {
				fmt.Printf("Are you sure you want to delete model %s? [y/N]: ", modelTag)
				var response string
				_, err := fmt.Scanln(&response)
				if err != nil {
					return fmt.Errorf("failed to read input: %w", err)
				}

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
