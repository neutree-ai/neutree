package model

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/pkg/client"
	"github.com/neutree-ai/neutree/pkg/model_registry/bentoml"
)

func NewPushCmd() *cobra.Command {
	var modelName string
	var version string
	var description string
	var labelsFlag []string

	cmd := &cobra.Command{
		Use:   "push [local_model_path]",
		Short: "Push a model to the registry",
		Long:  `Push a local model to the registry with specified metadata`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modelPath := args[0]

			// Check if file or directory exists
			info, err := os.Stat(modelPath)
			if err != nil {
				return fmt.Errorf("model path %s does not exist", modelPath)
			}

			// If model name is not specified, use directory name
			if modelName == "" {
				modelName = filepath.Base(modelPath)
			}

			// Auto‑generate version if not provided
			if version == "" {
				v, err := bentoml.GenerateVersion()
				if err != nil {
					return fmt.Errorf("failed to generate version: %w", err)
				}

				version = *v
			}

			// Parse labels
			labels := make(map[string]string)
			for _, label := range labelsFlag {
				key, value, err := parseLabel(label)
				if err != nil {
					return err
				}
				labels[key] = value
			}

			// If modelPath is a directory, tar‑gz it into a temp *.bentomodel
			if info.IsDir() {
				fmt.Printf("Creating archive for model %s...\n", modelName)

				archivePath, err := bentoml.CreateArchive(modelPath, modelName, version)
				if err != nil {
					return err
				}

				modelPath = archivePath
			}

			// Create client
			c := client.NewClient(serverURL, client.WithAPIKey(apiKey))

			fmt.Printf("Pushing model %s:%s to registry...\n", modelName, version)

			if err := c.Models.Push(workspace, registry, modelPath, modelName, version, description, labels); err != nil {
				return fmt.Errorf("failed to push model: %w", err)
			}

			fmt.Printf("Model %s:%s pushed successfully\n", modelName, version)
			return nil
		},
	}

	cmd.Flags().StringVarP(&modelName, "name", "n", "", "Name of the model (default: directory name)")
	cmd.Flags().StringVarP(&version, "version", "v", "", "Version of the model, (default: auto‑generated)")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Description of the model")
	cmd.Flags().StringSliceVarP(&labelsFlag, "label", "l", nil, "Labels in the format key=value")

	if err := cmd.MarkFlagRequired("name"); err != nil {
		panic(fmt.Sprintf("Failed to mark flag 'name' as required: %v", err))
	}

	return cmd
}

// parseLabel parses a "key=value" format string into key-value pair
func parseLabel(label string) (string, string, error) {
	key, value, found := strings.Cut(label, "=")
	if !found {
		return "", "", fmt.Errorf("invalid label format for %s, expected key=value", label)
	}

	return key, value, nil
}
