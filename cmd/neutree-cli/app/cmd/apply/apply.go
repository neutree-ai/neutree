package apply

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/resource"
	"github.com/neutree-ai/neutree/pkg/client"
	"github.com/neutree-ai/neutree/pkg/scheme"
)

type applyOptions struct {
	file        string
	forceUpdate bool
}

// NewApplyCmd creates the apply cobra command.
func NewApplyCmd() *cobra.Command {
	opts := &applyOptions{}

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply resources from a YAML file",
		Long: `Apply creates or updates resources defined in a multi-document YAML file.

By default, resources that already exist are skipped. Use --force-update to update existing resources.

Examples:
  # Apply resources from a file
  neutree-cli apply -f resources.yaml --server-url https://api.neutree.ai --api-key sk_xxx

  # Apply with force update for existing resources
  neutree-cli apply -f resources.yaml --server-url https://api.neutree.ai --api-key sk_xxx --force-update`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApply(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.file, "file", "f", "", "Path to the YAML file containing resources (required)")
	cmd.Flags().BoolVar(&opts.forceUpdate, "force-update", false, "Update resources that already exist (default: skip)")

	_ = cmd.MarkFlagRequired("file")

	return cmd
}

func runApply(opts *applyOptions) error {
	c, err := global.NewClient()
	if err != nil {
		return err
	}

	// Build scheme and decoder
	s, err := client.BuildScheme()
	if err != nil {
		return fmt.Errorf("failed to build scheme: %w", err)
	}

	decoder := scheme.NewCodecFactory(s).Decoder()

	// Read and parse YAML
	data, err := os.ReadFile(opts.file)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", opts.file, err)
	}

	resources, err := resource.ParseMultiDocYAML(data, decoder)
	if err != nil {
		return err
	}

	if len(resources) == 0 {
		fmt.Println("No resources found in file")
		return nil
	}

	// Sort by dependency order
	resource.SortByPriority(resources)

	// Apply each resource
	var hasError bool

	for _, res := range resources {
		kind := res.GetKind()
		name := res.GetName()
		workspace := res.GetWorkspace()

		label := resource.Label(kind, workspace, name)

		result, err := c.Generic.Exists(kind, workspace, name)
		if err != nil {
			fmt.Printf("%-50s failed (%v)\n", label, err)

			hasError = true

			continue
		}

		if result.Exists {
			if opts.forceUpdate {
				if err := c.Generic.Update(kind, result.ID, res); err != nil {
					fmt.Printf("%-50s failed (%v)\n", label, err)

					hasError = true

					continue
				}

				fmt.Printf("%-50s updated\n", label)
			} else {
				fmt.Printf("%-50s skipped (already exists)\n", label)
			}

			continue
		}

		if err := c.Generic.Create(kind, res); err != nil {
			fmt.Printf("%-50s failed (%v)\n", label, err)

			hasError = true

			continue
		}

		fmt.Printf("%-50s created\n", label)
	}

	if hasError {
		return fmt.Errorf("some resources failed to apply")
	}

	return nil
}
