package packageimport

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/cli/packageimport"
)

type ClusterImportOptions struct {
	packagePath string
	extractPath string
	importLocal bool
}

func NewClusterImportCmd() *cobra.Command {
	opts := &ClusterImportOptions{}

	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Import a cluster image package with cluster container images",
		Long: `Import a cluster image package into Neutree.

This command imports container images required for clusters. It performs the following steps:
  1. Extracts the cluster image package archive
  2. Parses and validates the manifest.yaml structure
  3. Loads container images from the package
  4. Pushes images to the configured image registry in the workspace

Package Requirements:
The package must be a tar.gz archive containing:
  • manifest.yaml - Package metadata and image definitions
  • images/*.tar  - Container image tar files

Example manifest.yaml:
---
manifest_version: "1.0"

metadata:
  description: "Cluster image package for Neutree"
  version: "v1.0.0"

images:
  - image_name: "neutree/neutree-serve"
    tag: "v1.0.0"
    image_file: "images/all-images.tar"
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClusterImport(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.packagePath, "package", "p", "", "Path to the cluster image package file (required)")
	cmd.Flags().StringVar(&opts.extractPath, "extract-path", "/tmp", "Path to extract package to (default: temporary directory)")
	cmd.Flags().BoolVar(&opts.importLocal, "local", false, "Skip pushing images to the registry, only load images locally")

	_ = cmd.MarkFlagRequired("package")

	return cmd
}

func runClusterImport(opts *ClusterImportOptions) error {
	ctx := context.Background()

	// Cluster no need to create apiclient
	importer := packageimport.NewImporter(nil)

	// Prepare import options
	importOpts := &packageimport.ImportOptions{
		PackagePath: opts.packagePath,
		ExtractPath: opts.extractPath,
	}

	// if not importLocal, set registry info
	if !opts.importLocal {
		importOpts.MirrorRegistry = mirrorRegistry
		importOpts.RegistryUser = registryUsername
		importOpts.RegistryPassword = registryPassword
	} else {
		importOpts.SkipImagePush = true
	}

	// Import the package
	klog.Infof("Importing cluster package: %s", opts.packagePath)

	result, err := importer.Import(ctx, importOpts)
	if err != nil {
		return fmt.Errorf("failed to import cluster package: %w", err)
	}

	// Print results
	fmt.Printf("\n✓ Successfully imported cluster package\n\n")

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
