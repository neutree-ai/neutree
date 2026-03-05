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

This command supports two input formats:
  • .tar.gz archive: Extracts and validates the full package including image file references
  • .yaml/.yml manifest: Validates manifest structure and engine configuration only

Validation checks:
  1. Parses and validates the manifest.yaml structure
  2. Checks manifest schema and required fields
  3. For archives: verifies that all referenced image files exist in the package

The command does not:
  • Load Docker images
  • Push images to registries
  • Create or modify engine definitions
  • Require API connectivity
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidate(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.PackagePath, "package", "p", "", "Path to engine package (.tar.gz) or manifest file (.yaml) (required)")
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
