package apply

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/pkg/client"
	"github.com/neutree-ai/neutree/pkg/scheme"
)

type applyOptions struct {
	file        string
	forceUpdate bool
}

// kindPriority defines the topological apply order.
// Lower values are applied first.
var kindPriority = map[string]int{
	"Workspace":      0,
	"Engine":         1,
	"ImageRegistry":  1,
	"ModelRegistry":  1,
	"Role":           1,
	"OEMConfig":      1,
	"Cluster":        2,
	"Endpoint":       3,
	"ModelCatalog":   3,
	"RoleAssignment": 3,
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

	resources, err := parseMultiDocYAML(data, decoder)
	if err != nil {
		return err
	}

	if len(resources) == 0 {
		fmt.Println("No resources found in file")
		return nil
	}

	// Sort by dependency order
	sortByPriority(resources)

	// Apply each resource
	var hasError bool

	for _, res := range resources {
		kind := res.GetKind()
		name := res.GetName()
		workspace := res.GetWorkspace()

		label := kind + "/" + name
		if workspace != "" {
			label = kind + "/" + workspace + "/" + name
		}

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

// parseMultiDocYAML splits a multi-document YAML file and decodes each document
// into the corresponding Go type via the scheme decoder.
func parseMultiDocYAML(data []byte, decoder scheme.Decoder) ([]scheme.Object, error) {
	var resources []scheme.Object

	yamlDecoder := yaml.NewDecoder(bytes.NewReader(data))

	for {
		var raw map[string]any
		if err := yamlDecoder.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}

			return nil, fmt.Errorf("failed to decode YAML document: %w", err)
		}

		if len(raw) == 0 {
			continue
		}

		// Normalize legacy field names
		if v, ok := raw["apiVersion"]; ok {
			if _, exists := raw["api_version"]; !exists {
				raw["api_version"] = v
				delete(raw, "apiVersion")
			}
		}

		jsonData, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("failed to convert YAML to JSON: %w", err)
		}

		obj, err := decoder.Decode(jsonData, "")
		if err != nil {
			return nil, fmt.Errorf("failed to decode resource: %w", err)
		}

		resources = append(resources, obj)
	}

	return resources, nil
}

// sortByPriority sorts resources by their dependency priority (stable sort).
func sortByPriority(resources []scheme.Object) {
	sort.SliceStable(resources, func(i, j int) bool {
		pi := priorityOf(resources[i].GetKind())
		pj := priorityOf(resources[j].GetKind())

		return pi < pj
	})
}

func priorityOf(kind string) int {
	if p, ok := kindPriority[kind]; ok {
		return p
	}

	// Unknown kinds go last
	return 99
}
