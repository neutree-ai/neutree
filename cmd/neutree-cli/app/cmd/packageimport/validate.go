package packageimport

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/cli/packageimport"
)

type ValidateOptions struct {
	PackagePath string
}

func NewValidateCmd() *cobra.Command {
	opts := &ValidateOptions{}

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a Neutree package structure without importing it",
		Long: `Validate a Neutree package without performing the actual import.

This command performs validation checks on a Neutree package to ensure it can be imported successfully:
  1. Extracts the package to a temporary directory
  2. Parses and validates the manifest.yaml structure
  3. Verifies that all referenced image files exist in the package
  4. Checks manifest schema and required fields

This is useful for:
  • Testing package integrity before importing
  • Debugging package creation issues
  • Verifying package format compliance

The command does not:
  • Load Docker images
  • Push images to registries
  • Create or modify engine definitions
  • Require API connectivity

This command can be used with engine, cluster, and control plane packages, but performs only generic manifest validation (not type-specific checks).
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidate(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.PackagePath, "package", "p", "", "Path to the neutree package file (required)")
	_ = cmd.MarkFlagRequired("package")

	return cmd
}

func runValidate(opts *ValidateOptions) error {
	klog.Infof("Validating neutree package: %s", opts.PackagePath)

	// Validate the package
	if err := packageimport.ValidatePackage(opts.PackagePath); err != nil {
		return fmt.Errorf("package validation failed: %w", err)
	}

	fmt.Printf("\n✓ Package validation successful\n\n")
	fmt.Printf("The package at '%s' is valid and ready to be imported.\n", opts.PackagePath)

	return nil
}
