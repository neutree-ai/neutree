package engine

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/pkg/client"
)

type removeVersionOptions struct {
	engineName string
	version    string
	force      bool
}

func NewRemoveVersionCmd() *cobra.Command {
	opts := &removeVersionOptions{}

	cmd := &cobra.Command{
		Use:   "remove-version",
		Short: "Remove a specific version from an engine",
		Long: `Remove a specific version from an engine definition.

This command removes a single version from the engine's version list.
If the engine has only one version, use --force to allow removal (which effectively
leaves the engine with no versions).

The command will check if any endpoints are currently using the version and
refuse to remove it unless --force is specified.

Examples:
  # Remove vllm v0.6.0
  neutree-cli engine remove-version --name vllm --version v0.6.0

  # Force remove even if endpoints are using it
  neutree-cli engine remove-version --name vllm --version v0.6.0 --force
`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemoveVersion(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.engineName, "name", "n", "", "Engine name (required)")
	cmd.Flags().StringVar(&opts.version, "version", "", "Engine version to remove (required)")
	cmd.Flags().BoolVarP(&opts.force, "force", "f", false, "Force removal even if version is in use or is the last version")

	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("version")

	return cmd
}

func runRemoveVersion(opts *removeVersionOptions) error {
	apiClient, err := global.NewClient()
	if err != nil {
		return err
	}

	// Get the engine
	engine, err := apiClient.Engines.Get(workspace, opts.engineName)
	if err != nil {
		return fmt.Errorf("failed to get engine %q: %w", opts.engineName, err)
	}

	if engine.Spec == nil || len(engine.Spec.Versions) == 0 {
		return fmt.Errorf("engine %q has no versions", opts.engineName)
	}

	// Find the version index
	versionIdx := -1
	for i, v := range engine.Spec.Versions {
		if v.Version == opts.version {
			versionIdx = i
			break
		}
	}

	if versionIdx == -1 {
		return fmt.Errorf("version %q not found in engine %q", opts.version, opts.engineName)
	}

	// Check if this is the last version
	if len(engine.Spec.Versions) == 1 && !opts.force {
		return fmt.Errorf("version %q is the only version of engine %q; use --force to remove it", opts.version, opts.engineName)
	}

	// Check if any endpoints are using this version
	if !opts.force {
		inUse, err := isVersionInUse(apiClient.Generic, workspace, opts.engineName, opts.version)
		if err != nil {
			return fmt.Errorf("failed to check if version is in use: %w", err)
		}

		if inUse {
			return fmt.Errorf("version %q of engine %q is in use by one or more endpoints; use --force to remove it anyway", opts.version, opts.engineName)
		}
	}

	// Remove the version
	engine.Spec.Versions = append(engine.Spec.Versions[:versionIdx], engine.Spec.Versions[versionIdx+1:]...)

	// Recalculate SupportedTasks from remaining versions
	engine.Spec.SupportedTasks = collectSupportedTasks(engine.Spec.Versions)

	// Update the engine
	if err := apiClient.Engines.Update(engine.GetID(), engine); err != nil {
		return fmt.Errorf("failed to update engine %q: %w", opts.engineName, err)
	}

	fmt.Printf("Successfully removed version %q from engine %q\n", opts.version, opts.engineName)

	return nil
}

// collectSupportedTasks computes the union of SupportedTasks across all remaining versions.
func collectSupportedTasks(versions []*v1.EngineVersion) []string {
	seen := make(map[string]struct{})

	for _, v := range versions {
		for _, task := range v.SupportedTasks {
			seen[task] = struct{}{}
		}
	}

	tasks := make([]string, 0, len(seen))
	for task := range seen {
		tasks = append(tasks, task)
	}

	return tasks
}

// isVersionInUse checks if any endpoint in the workspace references the given engine name and version.
func isVersionInUse(genericSvc *client.GenericService, ws, engineName, version string) (bool, error) {
	items, err := genericSvc.List("Endpoint", ws)
	if err != nil {
		return false, err
	}

	for _, item := range items {
		var ep v1.Endpoint
		if err := json.Unmarshal(item, &ep); err != nil {
			continue
		}

		if ep.Spec != nil && ep.Spec.Engine != nil {
			if ep.Spec.Engine.Engine == engineName && ep.Spec.Engine.Version == version {
				return true, nil
			}
		}
	}

	return false, nil
}
