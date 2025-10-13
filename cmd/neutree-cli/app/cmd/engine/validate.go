package engine

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/engine_version"
)

type ValidateOptions struct {
	PackagePath string
}

func NewValidateCmd() *cobra.Command {
	opts := &ValidateOptions{}

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate an engine version package",
		Long: `Validate an engine version package without importing it.

This command will:
  1. Extract the package to a temporary directory
  2. Parse and validate the manifest file
  3. Check that all referenced image files exist
  4. Verify the package structure is correct

This is useful for testing packages before importing them.

Examples:
  # Validate a package
  neutree-cli engine validate --package vllm-v0.5.0.tar.gz

  # Check package structure
  neutree-cli engine validate -p /path/to/engine-package.tar.gz
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidate(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.PackagePath, "package", "p", "", "Path to the engine version package file (required)")
	_ = cmd.MarkFlagRequired("package")

	return cmd
}

func runValidate(opts *ValidateOptions) error {
	klog.Infof("Validating engine version package: %s", opts.PackagePath)

	// Validate the package
	if err := engine_version.ValidatePackage(opts.PackagePath); err != nil {
		return fmt.Errorf("package validation failed: %w", err)
	}

	fmt.Printf("\n✓ Package validation successful\n\n")
	fmt.Printf("The package at '%s' is valid and ready to be imported.\n", opts.PackagePath)

	return nil
}
