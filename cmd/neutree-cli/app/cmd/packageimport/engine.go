package packageimport

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	engine "github.com/neutree-ai/neutree/internal/cli/packageimport"
	"github.com/neutree-ai/neutree/pkg/client"
)

type EngineImportOptions struct {
	packagePath   string
	skipImagePush bool
	force         bool
	extractPath   string
}

func NewEngineImportCmd() *cobra.Command {
	opts := &EngineImportOptions{}

	cmd := &cobra.Command{
		Use:   "engine",
		Short: "Import an engine version package with model serving images and engine definitions",
		Long: `Import an engine version package into Neutree.

This command supports two input formats:

1. Archive mode (.tar.gz): Traditional full package with images
   - Extracts the archive, loads and pushes container images, registers engine metadata

2. Manifest mode (.yaml/.yml): Standalone manifest file for fast engine registration
   - With --skip-image-push: Registers engine metadata only (no image handling)
   - With package_url in manifest: Downloads the full archive, extracts, pushes images
   - Without package_url: Registers engine metadata only

Import steps (archive mode):
  1. Extracts the engine version package archive
  2. Parses and validates the manifest.yaml structure
  3. Loads container images for different accelerators (CUDA, ROCm, CPU)
  4. Pushes images to the configured image registry in the workspace
  5. Creates or updates the engine definition with the new version

Use --force to overwrite existing engine versions.
Use --skip-image-push to only update engine definitions without pushing images.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEngineImport(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.packagePath, "package", "p", "", "Path to engine package (.tar.gz) or manifest file (.yaml/.yml) (required)")
	cmd.Flags().BoolVar(&opts.skipImagePush, "skip-image-push", false, "Skip loading and pushing images (metadata-only import)")
	cmd.Flags().BoolVarP(&opts.force, "force", "f", false, "Force overwrite if engine version already exists")
	cmd.Flags().StringVar(&opts.extractPath, "extract-path", "", "Path to extract package to (default: temporary directory)")

	_ = cmd.MarkFlagRequired("package")

	return cmd
}

func runEngineImport(opts *EngineImportOptions) error {
	ctx := context.Background()

	// Validate API connection
	if global.ServerURL == "" {
		return fmt.Errorf("server URL is required (use --server-url or set NEUTREE_SERVER_URL)")
	}

	// Initialize API client
	klog.Info("Initializing API client...")

	clientOpts := []client.ClientOption{}
	if global.APIKey != "" {
		clientOpts = append(clientOpts, client.WithAPIKey(global.APIKey))
	}

	apiClient := client.NewClient(global.ServerURL, clientOpts...)

	// Create importer with engines service
	importer := engine.NewImporter(apiClient)

	// Prepare import options
	importOpts := &engine.ImportOptions{
		PackagePath:      opts.packagePath,
		MirrorRegistry:   mirrorRegistry,
		RegistryProject:  registryProject,
		RegistryUser:     registryUsername,
		RegistryPassword: registryPassword,
		Workspace:        workspace,
		SkipImagePush:    opts.skipImagePush,
		SkipImageLoad:    opts.skipImagePush,
		Force:            opts.force,
		ExtractPath:      opts.extractPath,
	}

	// Import the package
	klog.Infof("Importing engine version package: %s", opts.packagePath)

	result, err := importer.Import(ctx, importOpts)
	if err != nil {
		return fmt.Errorf("failed to import engine version package: %w", err)
	}

	// Print results
	fmt.Printf("\n✓ Successfully imported engine version package\n\n")

	if len(result.ImagesImported) > 0 {
		fmt.Printf("\nImages Imported:\n")

		for _, img := range result.ImagesImported {
			fmt.Printf("  • %s\n", img)
		}
	}

	if len(result.EnginesImported) > 0 {
		fmt.Printf("\nEngines Imported:\n")

		for _, eng := range result.EnginesImported {
			for _, ver := range eng.EngineVersions {
				fmt.Printf("  • %s:%s\n", eng.Name, ver.Version)
			}
		}
	}

	if len(result.Errors) > 0 {
		fmt.Printf("\nWarnings/Errors:\n")

		for _, e := range result.Errors {
			fmt.Printf("  ⚠ %s\n", e.Error())
		}
	}

	return nil
}
