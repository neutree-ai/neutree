package packageimport

import "github.com/spf13/cobra"

var (
	serverURL        string
	apiKey           string
	mirrorRegistry   string
	registryUsername string
	registryPassword string
	workspace        string
)

func NewImportCmd() *cobra.Command {
	importCmd := &cobra.Command{
		Use:   "import",
		Short: "Import Neutree packages including engines, clusters, and control plane components",
		Long: `Import Neutree packages into the system.

This command provides subcommands to import different types of Neutree packages:
  • engine       - Import engine version packages with model serving images
  • cluster      - Import cluster image packages for compute clusters
  • controlplane - Import control plane component images
  • validate     - Validate package structure without importing

All packages follow a standard format containing a manifest.yaml and container images.
Use the appropriate subcommand based on the package type you want to import.
`,
	}

	// Add global flags
	importCmd.PersistentFlags().StringVar(&serverURL, "server-url", "", "Server URL")
	importCmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key")
	importCmd.PersistentFlags().StringVar(&workspace, "workspace", "default", "Workspace")
	importCmd.PersistentFlags().StringVar(&mirrorRegistry, "mirror-registry", "", "Container image registry to push images to (if required)")
	importCmd.PersistentFlags().StringVar(&registryUsername, "registry-username", "", "Username for the container image registry (if required)")
	importCmd.PersistentFlags().StringVar(&registryPassword, "registry-password", "", "Password for the container image registry (if required)")

	importCmd.AddCommand(NewClusterImportCmd())
	importCmd.AddCommand(NewEngineImportCmd())
	importCmd.AddCommand(NewValidateCmd())
	importCmd.AddCommand(NewControlPlaneImportCmd())

	return importCmd
}
