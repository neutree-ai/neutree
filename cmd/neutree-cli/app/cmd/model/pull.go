package model

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/pkg/client"
)

func NewPullCmd() *cobra.Command {
	var outputDir string
	var force bool

	cmd := &cobra.Command{
		Use:   "pull [model_name:version]",
		Short: "Pull a model from the registry",
		Long:  `Download a model from the registry to local directory`,
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

			// If output directory is not specified, use current directory
			if outputDir == "" {
				outputDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get current directory: %w", err)
				}
			}

			// Check if target path already exists
			targetPath := filepath.Join(outputDir, modelTag)
			if _, err := os.Stat(targetPath); err == nil && !force {
				return fmt.Errorf("target path %s already exists, use --force to overwrite", targetPath)
			}

			fmt.Printf("Pulling model %s to %s...\n", modelTag, outputDir)

			if err := c.Models.Pull(workspace, registry, modelName, version, outputDir); err != nil {
				return fmt.Errorf("failed to pull model %s: %w", modelTag, err)
			}

			fmt.Printf("Model %s pulled successfully to %s\n", modelTag, outputDir)
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputDir, "output", "o", "", "Output directory (default: current directory)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force overwrite if destination already exists")

	return cmd
}
