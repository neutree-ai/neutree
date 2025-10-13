package engine

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/client"
	"github.com/neutree-ai/neutree/pkg/engine_version"
)

type ImportOptions struct {
	packagePath   string
	registry      string
	workspace     string
	skipImagePush bool
	force         bool
	extractPath   string
}

func NewImportCmd() *cobra.Command {
	opts := &ImportOptions{}

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import an engine version package",
		Long: `Import an engine version package into Neutree.

This command will:
  1. Extract the engine version package
  2. Parse the manifest and validate the package structure
  3. Load container images from the package
  4. Push images to the specified registry (unless --skip-image-push is set)
  5. Update or create the engine definition with the new version

The package must be a tar.gz, zip, or tar file containing:
  • manifest.yaml - Package metadata and engine version definition
  • images/*.tar - Container images for different accelerators

Example manifest.yaml structure:
---
manifest_version: "1.0"
package:
  metadata:
    engine_name: "vllm"
    version: "v0.5.0"
    description: "vLLM engine with CUDA support"
    package_version: "1.0"
  images:
    - accelerator: "nvidia-gpu"
      image_name: "neutree/vllm-cuda"
      tag: "v0.5.0"
      image_file: "images/vllm-cuda-v0.5.0.tar"
      platform: "linux/amd64"
  engine_version:
    version: "v0.5.0"
    values_schema:
      type: "object"
      properties:
        gpu_memory_utilization:
          type: "number"
          default: 0.9
    deploy_template:
      kubernetes:
        default: base64-encoded-yaml-string

Examples:
  # Import with image push
  neutree-cli engine import --package vllm-v0.5.0.tar.gz \
    --registry test \
    --workspace default \
    --server-url http://localhost:8080 \
    --api-key your-api-key

  # Import without pushing images (for testing/development)
  neutree-cli engine import --package vllm-v0.5.0.tar.gz --skip-image-push

  # Force overwrite existing version
  neutree-cli engine import --package vllm-v0.5.0.tar.gz \
  neutree-cli engine import --package vllm-v0.5.0.tar.gz \
    --registry test \
    --workspace default \
    --server-url http://localhost:8080 \
    --api-key your-api-key \
    --force
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.packagePath, "package", "p", "", "Path to the engine version package file (required)")
	cmd.Flags().StringVarP(&opts.registry, "registry", "r", "", "Container registry to push images to (e.g., registry.example.com)")
	cmd.Flags().StringVarP(&opts.workspace, "workspace", "w", "default", "Workspace to import the engine to")
	cmd.Flags().BoolVar(&opts.skipImagePush, "skip-image-push", false, "Skip pushing images to registry")
	cmd.Flags().BoolVarP(&opts.force, "force", "f", false, "Force overwrite if engine version already exists")
	cmd.Flags().StringVar(&opts.extractPath, "extract-path", "", "Path to extract package to (default: temporary directory)")

	_ = cmd.MarkFlagRequired("package")

	return cmd
}

func runImport(opts *ImportOptions) error {
	ctx := context.Background()

	// Validate API connection
	if serverURL == "" {
		return fmt.Errorf("API URL is required (use --api-url or set NEUTREE_API_URL env var)")
	}

	// Initialize API client
	klog.Info("Initializing API client...")

	clientOpts := []client.ClientOption{}
	if apiKey != "" {
		clientOpts = append(clientOpts, client.WithAPIKey(apiKey))
	}

	apiClient := client.NewClient(serverURL, clientOpts...)

	// Create importer with engines service
	importer := engine_version.NewImporter(apiClient)

	// Prepare import options
	importOpts := &engine_version.ImportOptions{
		PackagePath:   opts.packagePath,
		ImageRegistry: opts.registry,
		Workspace:     opts.workspace,
		SkipImagePush: opts.skipImagePush,
		Force:         opts.force,
		ExtractPath:   opts.extractPath,
	}

	// Import the package
	klog.Infof("Importing engine version package: %s", opts.packagePath)

	result, err := importer.Import(ctx, importOpts)
	if err != nil {
		return fmt.Errorf("failed to import engine version package: %w", err)
	}

	// Print results
	fmt.Printf("\n✓ Successfully imported engine version package\n\n")
	fmt.Printf("Engine Name:    %s\n", result.EngineName)
	fmt.Printf("Version:        %s\n", result.Version)
	fmt.Printf("Engine Updated: %v\n", result.EngineUpdated)

	if len(result.ImagesImported) > 0 {
		fmt.Printf("\nImages Imported:\n")

		for _, img := range result.ImagesImported {
			fmt.Printf("  • %s\n", img)
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
