package packageimport

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/cli/packageimport"
)

type ControlPlaneImportOptions struct {
	packagePath string
	extractPath string

	mirrorRegistry   string
	registryUsername string
	registryPassword string
	importLocal      bool
}

func NewControlPlaneImportCmd() *cobra.Command {
	opts := &ControlPlaneImportOptions{}

	cmd := &cobra.Command{
		Use:   "controlplane",
		Short: "Import a control plane image package with Neutree system components",
		Long: `Import a control plane image package into Neutree.

This command imports container images for Neutree control plane components. It performs:
  1. Extracts the control plane image package archive
  2. Parses and validates the manifest.yaml structure
  3. Loads container images from the package
  4. Optionally pushes images to a mirror registry or loads them locally

Package Requirements:
The package must be a tar.gz archive containing:
  • manifest.yaml - Package metadata and image definitions
  • images/*.tar  - Container image tar files for control plane components

Example manifest.yaml:
---
manifest_version: "1.0"

metadata:
  description: "Control plane image package for Neutree"
  version: "v1.0.0"

images:
  - image_name: "neutree/neutree-api"
    tag: "v1.0.0"
    image_file: "images/all-images.tar"

Note: This command does not require API connection and can run standalone.
Use --local flag to skip registry push and only load images to local Docker.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runControlPlaneImport(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.packagePath, "package", "p", "", "Path to the control plane version package file (required)")
	cmd.Flags().StringVar(&opts.extractPath, "extract-path", "/tmp", "Path to extract package to (default: temporary directory)")
	cmd.Flags().StringVar(&opts.mirrorRegistry, "mirror-registry", "", "Container image registry to push images to (required)")
	cmd.Flags().StringVar(&opts.registryUsername, "registry-username", "", "Username for the container image registry (if required)")
	cmd.Flags().StringVar(&opts.registryPassword, "registry-password", "", "Password for the container image registry (if required)")
	cmd.Flags().BoolVar(&opts.importLocal, "local", false, "Skip pushing images to the registry, only load images locally")

	_ = cmd.MarkFlagRequired("package")

	return cmd
}

func runControlPlaneImport(opts *ControlPlaneImportOptions) error {
	ctx := context.Background()

	// ControlPlane no need to create apiclient
	importer := packageimport.NewImporter(nil)

	// Prepare import options
	importOpts := &packageimport.ImportOptions{
		PackagePath: opts.packagePath,
		Workspace:   workspace,
		ExtractPath: opts.extractPath,
	}

	// if skipImagePush is not set, configure registry info
	if !opts.importLocal {
		importOpts.MirrorRegistry = opts.mirrorRegistry
		importOpts.RegistryUser = opts.registryUsername
		importOpts.RegistryPassword = opts.registryPassword
	} else {
		importOpts.SkipImagePush = true
	}

	// Import the package
	klog.Infof("Importing cluster package: %s", opts.packagePath)

	result, err := importer.Import(ctx, importOpts)
	if err != nil {
		return fmt.Errorf("failed to import control plane package: %w", err)
	}

	// Print results
	fmt.Printf("\n✓ Successfully imported control plane package\n\n")

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
