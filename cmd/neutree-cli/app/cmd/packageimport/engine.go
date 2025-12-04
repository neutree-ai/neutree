package packageimport

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

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

This command imports engine versions that define model serving capabilities. It performs:
  1. Extracts the engine version package archive
  2. Parses and validates the manifest.yaml structure
  3. Loads container images for different accelerators (CUDA, ROCm, CPU)
  4. Pushes images to the configured image registry in the workspace
  5. Creates or updates the engine definition with the new version

Package Requirements:
The package must be a tar.gz archive containing:
  • manifest.yaml - Engine metadata, version definitions, and container images
  • images/*.tar  - Container image tar files for different accelerators

Example manifest.yaml:
---
manifest_version: "1.0"

metadata:
  description: "vLLM engine package for LLM inference"
  version: "v0.6.0"

engines:
  - name: "vllm"
    supported_tasks: ["text-generation"]
    engine_versions:
      - version: "v0.6.0"
        images:
          - image_name: "neutree/vllm-cuda"
            tag: "v0.6.0"
            accelerator: "nvidia.com/gpu"
          - image_name: "neutree/vllm-rocm"
            tag: "v0.6.0"
            accelerator: "amd.com/gpu"

images:
  - image_name: "neutree/vllm-cuda"
    tag: "v0.6.0"
    image_file: "images/vllm-cuda.tar"
  - image_name: "neutree/vllm-rocm"
    tag: "v0.6.0"
    image_file: "images/vllm-rocm.tar"

Use --force to overwrite existing engine versions.
Use --skip-image-push to only update engine definitions without pushing images.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEngineImport(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.packagePath, "package", "p", "", "Path to the engine version package file (required)")
	cmd.Flags().BoolVar(&opts.skipImagePush, "skip-image-push", false, "Skip pushing images to registry")
	cmd.Flags().BoolVarP(&opts.force, "force", "f", false, "Force overwrite if engine version already exists")
	cmd.Flags().StringVar(&opts.extractPath, "extract-path", "", "Path to extract package to (default: temporary directory)")

	_ = cmd.MarkFlagRequired("package")

	return cmd
}

func runEngineImport(opts *EngineImportOptions) error {
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
	importer := engine.NewImporter(apiClient)

	// Prepare import options
	importOpts := &engine.ImportOptions{
		PackagePath:   opts.packagePath,
		ImageRegistry: registry,
		Workspace:     workspace,
		SkipImagePush: opts.skipImagePush,
		SkipImageLoad: opts.skipImagePush,
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
