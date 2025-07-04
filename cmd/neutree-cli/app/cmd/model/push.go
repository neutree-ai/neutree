package model

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/schollz/progressbar/v3"
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
		Use:          "push [local_model_path]",
		Short:        "Push a model to the registry",
		Long:         `Push a local model to the registry with specified metadata`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
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
				// Calculate directory size for progress bar
				totalSize, _, err := calculateDirectorySize(modelPath)
				if err != nil {
					return fmt.Errorf("failed to calculate directory size: %w", err)
				}

				// Add some buffer for YAML file modifications and compression overhead
				totalSize = totalSize + 100*1024

				// Create progress bar for archive
				archiveBar := progressbar.DefaultBytes(totalSize, "Creating archive")

				archivePath, err := bentoml.CreateArchiveWithProgress(modelPath, modelName, version, archiveBar)
				if err != nil {
					return fmt.Errorf("failed to create archive: %w", err)
				}

				modelPath = archivePath
				defer os.Remove(archivePath)
			}

			// Create client
			c := client.NewClient(serverURL, client.WithAPIKey(apiKey), client.WithTimeout(0))

			// Get file size for progress bar
			fileInfo, err := os.Stat(modelPath)
			if err != nil {
				return fmt.Errorf("failed to get model file info: %w", err)
			}

			// Create progress bar for upload
			uploadBar := progressbar.DefaultBytes(fileInfo.Size(), "Uploading model")

			if err := c.Models.PushWithProgress(workspace, registry, modelPath, modelName, version, description, labels, uploadBar); err != nil {
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

// calculateDirectorySize calculates the total size of all files in a directory
func calculateDirectorySize(dir string) (int64, int, error) {
	var totalSize int64
	var fileCount int

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			totalSize += info.Size()
			fileCount++
		}

		return nil
	})

	return totalSize, fileCount, err
}

// parseLabel parses a "key=value" format string into key-value pair
func parseLabel(label string) (string, string, error) {
	key, value, found := strings.Cut(label, "=")
	if !found {
		return "", "", fmt.Errorf("invalid label format for %s, expected key=value", label)
	}

	return key, value, nil
}
