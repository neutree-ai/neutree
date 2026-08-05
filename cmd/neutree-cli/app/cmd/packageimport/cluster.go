package packageimport

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/internal/cli/packageimport"
	"github.com/neutree-ai/neutree/pkg/client"
)

type ClusterImportOptions struct {
	packagePath string
	extractPath string
	importLocal bool
	forceUpdate bool
}

type clusterPackageImporter interface {
	Import(context.Context, *packageimport.ImportOptions) (*packageimport.ImportResult, error)
}

var (
	clusterImportNewAPIClient = func() (*client.Client, error) {
		return global.NewClient()
	}
	clusterImportNewImporter = func(apiClient *client.Client) clusterPackageImporter {
		return packageimport.NewImporter(apiClient)
	}
)

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
	cmd.Flags().StringVar(&opts.extractPath, "extract-path", "",
		"Parent directory for extraction; a unique subdirectory is created automatically (default: system temp dir)")
	cmd.Flags().BoolVar(&opts.importLocal, "local", false, "Skip pushing images to the registry, only load images locally")
	cmd.Flags().BoolVar(&opts.forceUpdate, "force-update", false, "Overwrite an existing exact cluster profile")

	_ = cmd.MarkFlagRequired("package")

	return cmd
}

func runClusterImport(opts *ClusterImportOptions) error {
	ctx := context.Background()

	apiClient, err := clusterImportNewAPIClient()
	if err != nil {
		return err
	}
	importer := clusterImportNewImporter(apiClient)

	// Prepare import options
	importOpts := &packageimport.ImportOptions{
		PackagePath: opts.packagePath,
		Workspace:   workspace,
		ExtractPath: opts.extractPath,
		ForceUpdate: opts.forceUpdate,
	}

	// if not importLocal, set registry info
	if !opts.importLocal {
		importOpts.MirrorRegistry = mirrorRegistry
		importOpts.RegistryProject = registryProject
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
