package delete

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/resource"
	"github.com/neutree-ai/neutree/pkg/client"
	"github.com/neutree-ai/neutree/pkg/scheme"
)

type deleteOptions struct {
	file           string
	workspace      string
	ignoreNotFound bool
	force          bool
	wait           bool
	timeout        time.Duration
	interval       time.Duration
}

// NewDeleteCmd creates the delete cobra command.
func NewDeleteCmd() *cobra.Command {
	opts := &deleteOptions{}

	cmd := &cobra.Command{
		Use:   "delete <KIND> <NAME> | -f <file>",
		Short: "Delete resources by name or from a YAML file",
		Long: `Delete removes resources from the server.

Resources can be specified either by kind and name, or by providing a YAML file
containing resource definitions (the same format used by apply).

By default, the command waits for each resource to be fully deleted before
returning. Use --wait=false to return immediately after issuing the delete request.

Examples:
  # Delete a single endpoint
  neutree-cli delete Endpoint my-ep -w default --server-url https://api.neutree.ai --api-key sk_xxx

  # Delete resources defined in a YAML file
  neutree-cli delete -f resources.yaml --server-url https://api.neutree.ai --api-key sk_xxx

  # Delete without waiting for completion
  neutree-cli delete Endpoint my-ep -w default --wait=false --server-url https://api.neutree.ai --api-key sk_xxx

  # Ignore resources that don't exist
  neutree-cli delete Endpoint my-ep -w default --ignore-not-found --server-url https://api.neutree.ai --api-key sk_xxx`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDelete(opts, args)
		},
	}

	cmd.Flags().StringVarP(&opts.file, "file", "f", "", "Path to a YAML file containing resources to delete")
	cmd.Flags().StringVarP(&opts.workspace, "workspace", "w", "default", "Workspace name (only for kind+name mode; ignored for Workspace kind)")
	cmd.Flags().BoolVar(&opts.ignoreNotFound, "ignore-not-found", false, "Treat not-found resources as successful deletes")
	cmd.Flags().BoolVar(&opts.force, "force", false, "Force delete, skipping graceful shutdown")
	cmd.Flags().BoolVar(&opts.wait, "wait", true, "Wait for resources to be fully deleted")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 5*time.Minute, "Maximum time to wait for deletion")
	cmd.Flags().DurationVar(&opts.interval, "interval", 5*time.Second, "Poll interval when waiting for deletion")

	return cmd
}

func runDelete(opts *deleteOptions, args []string) error {
	if err := validateArgs(opts, args); err != nil {
		return err
	}

	c, err := global.NewClient()
	if err != nil {
		return err
	}

	if opts.file != "" {
		return runDeleteFromFile(c, opts)
	}

	return runDeleteByName(c, opts, args)
}

func validateArgs(opts *deleteOptions, args []string) error {
	if opts.file != "" && len(args) > 0 {
		return fmt.Errorf("cannot specify both -f/--file and positional arguments")
	}

	if opts.file == "" && len(args) != 2 {
		return fmt.Errorf("exactly 2 arguments required: <KIND> <NAME> (or use -f to specify a file)")
	}

	return nil
}

// runDeleteByName handles mode A: delete by kind + name.
func runDeleteByName(c *client.Client, opts *deleteOptions, args []string) error {
	kind, err := c.Generic.ResolveKind(args[0])
	if err != nil {
		return err
	}

	name := args[1]
	workspace := opts.workspace

	// Workspace kind does not belong to a workspace; blank it for display and API calls.
	if kind == "Workspace" {
		workspace = ""
	}

	label := resource.Label(kind, workspace, name)

	result, err := c.Generic.Exists(kind, workspace, name)
	if err != nil {
		return fmt.Errorf("failed to check %s: %w", label, err)
	}

	if !result.Exists {
		if opts.ignoreNotFound {
			fmt.Printf("%-50s not found (skipped)\n", label)
			return nil
		}

		return fmt.Errorf("%s not found", label)
	}

	if err := c.Generic.Delete(kind, result.ID, workspace, name, client.DeleteOptions{Force: opts.force}); err != nil {
		return fmt.Errorf("failed to delete %s: %w", label, err)
	}

	if opts.wait {
		fmt.Printf("%-50s deleting\n", label)

		if err := WaitForDeletion(c, kind, workspace, name, opts.timeout, opts.interval); err != nil {
			return err
		}

		fmt.Printf("%-50s deleted\n", label)
	} else {
		fmt.Printf("%-50s deleting\n", label)
	}

	return nil
}

// runDeleteFromFile handles mode B: delete from YAML file.
func runDeleteFromFile(c *client.Client, opts *deleteOptions) error {
	s, err := client.BuildScheme()
	if err != nil {
		return fmt.Errorf("failed to build scheme: %w", err)
	}

	decoder := scheme.NewCodecFactory(s).Decoder()

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

	// Sort in reverse dependency order (dependents first)
	resource.SortByReversePriority(resources)

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

		if !result.Exists {
			if opts.ignoreNotFound {
				fmt.Printf("%-50s not found (skipped)\n", label)
			} else {
				fmt.Printf("%-50s not found\n", label)

				hasError = true
			}

			continue
		}

		if err := c.Generic.Delete(kind, result.ID, workspace, name, client.DeleteOptions{Force: opts.force}); err != nil {
			fmt.Printf("%-50s failed (%v)\n", label, err)

			hasError = true

			continue
		}

		if opts.wait {
			fmt.Printf("%-50s deleting\n", label)

			if err := WaitForDeletion(c, kind, workspace, name, opts.timeout, opts.interval); err != nil {
				fmt.Printf("%-50s wait failed (%v)\n", label, err)

				hasError = true

				continue
			}

			fmt.Printf("%-50s deleted\n", label)
		} else {
			fmt.Printf("%-50s deleting\n", label)
		}
	}

	if hasError {
		return fmt.Errorf("some resources failed to delete")
	}

	return nil
}

// WaitForDeletion polls until the resource no longer exists or the timeout expires.
func WaitForDeletion(c *client.Client, kind, workspace, name string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)

	var lastErr error

	for time.Now().Before(deadline) {
		_, err := c.Generic.Get(kind, workspace, name)
		if err != nil {
			if client.IsNotFound(err) {
				return nil
			}

			lastErr = err
		} else {
			lastErr = nil
		}

		time.Sleep(interval)
	}

	return errors.Join(fmt.Errorf("timeout waiting for %s/%s to be deleted", kind, name), lastErr)
}
