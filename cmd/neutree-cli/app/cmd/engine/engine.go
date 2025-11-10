package engine

import (
	"github.com/spf13/cobra"
)

var (
	serverURL string
	apiKey    string
)

func NewEngineCmd() *cobra.Command {
	engineCmd := &cobra.Command{
		Use:   "engine",
		Short: "Manage Neutree engines",
		Long: `Manage Neutree engine versions, including importing engine version packages.

An engine version package contains:
  • Engine version metadata
  • Container images for different accelerators
  • Values schema for configuration
  • Deploy templates for different cluster types and modes

Examples:
  # Import an engine version package
  neutree-cli engine import --package vllm-v0.5.0.tar.gz --registry registry.example.com --workspace default

  # Import without pushing images (for testing)
  neutree-cli engine import --package vllm-v0.5.0.tar.gz --skip-image-push

  # Validate an engine version package
  neutree-cli engine validate --package vllm-v0.5.0.tar.gz

  # Force overwrite existing version
  neutree-cli engine import --package vllm-v0.5.0.tar.gz --registry registry.example.com --force
`,
	}

	// Add global flags
	engineCmd.PersistentFlags().StringVar(&serverURL, "server-url", "", "Server URL")
	engineCmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key")

	engineCmd.AddCommand(NewImportCmd())
	engineCmd.AddCommand(NewValidateCmd())

	return engineCmd
}
