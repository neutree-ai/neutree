package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/delete"
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

The command always checks if any endpoints are using the version and refuses
to remove it if so. Endpoints must be updated or deleted before the version
can be removed.

Examples:
  # Remove vllm v0.6.0
  neutree-cli engine remove-version --name vllm --version v0.6.0

  # Force remove the last remaining version
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
	cmd.Flags().BoolVarP(&opts.force, "force", "f", false, "Force removal even if it is the last version")

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

	// Check if any endpoints are using this version (always enforced, cannot be bypassed)
	inUse, endpoints, err := findEndpointsUsingVersion(apiClient.Generic, workspace, opts.engineName, opts.version)
	if err != nil {
		return fmt.Errorf("failed to check if version is in use: %w", err)
	}

	if inUse {
		return fmt.Errorf("cannot remove version %q of engine %q: in use by endpoint(s): %s", opts.version, opts.engineName, strings.Join(endpoints, ", "))
	}

	// If this is the last version, soft-delete the entire engine and wait for it to be removed
	if len(engine.Spec.Versions) == 1 {
		if err := apiClient.Generic.Delete("Engine", engine.GetID(), workspace, opts.engineName, client.DeleteOptions{}); err != nil {
			return fmt.Errorf("failed to delete engine %q: %w", opts.engineName, err)
		}

		// Wait for the controller to fully remove the engine
		if err := delete.WaitForDeletion(apiClient, "Engine", workspace, opts.engineName, 2*time.Minute, 3*time.Second); err != nil {
			return fmt.Errorf("engine %q deletion initiated but did not complete: %w", opts.engineName, err)
		}

		fmt.Printf("Successfully removed version %q and deleted engine %q (was the last version)\n", opts.version, opts.engineName)

		return nil
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

	sort.Strings(tasks)

	return tasks
}

// findEndpointsUsingVersion returns whether any endpoints use the given engine version,
// along with the names of those endpoints.
func findEndpointsUsingVersion(genericSvc *client.GenericService, ws, engineName, version string) (bool, []string, error) {
	items, err := genericSvc.List("Endpoint", ws)
	if err != nil {
		return false, nil, err
	}

	return matchEndpointsByEngineVersion(items, engineName, version)
}

// matchEndpointsByEngineVersion checks which endpoints in the given JSON items
// reference the specified engine name and version, returning their names.
func matchEndpointsByEngineVersion(items []json.RawMessage, engineName, version string) (bool, []string, error) {
	var names []string

	for _, item := range items {
		var ep v1.Endpoint
		if err := json.Unmarshal(item, &ep); err != nil {
			return false, nil, fmt.Errorf("failed to decode endpoint: %w", err)
		}

		if ep.Spec != nil && ep.Spec.Engine != nil {
			if ep.Spec.Engine.Engine == engineName && ep.Spec.Engine.Version == version {
				names = append(names, ep.GetName())
			}
		}
	}

	return len(names) > 0, names, nil
}
