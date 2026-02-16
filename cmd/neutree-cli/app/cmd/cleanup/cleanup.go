package cleanup

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/launch"
	"github.com/neutree-ai/neutree/pkg/command"
)

var validComponents = map[string]bool{
	"neutree-core": true,
	"obs-stack":    true,
}

type cleanupOptions struct {
	removeData bool
	force      bool
}

func NewCleanupCmd() *cobra.Command {
	opts := &cleanupOptions{}

	cmd := &cobra.Command{
		Use:   "cleanup <neutree-core|obs-stack>",
		Short: "Remove Neutree components deployed by launch",
		Long: `Remove Neutree components that were previously deployed using the launch command.

This stops and removes the Docker Compose project for the specified component.
By default, persistent data (volumes) is preserved. Use --remove-data to also
delete volumes.

A confirmation prompt is shown before proceeding. Use --force to skip it.

Examples:
  # Remove neutree-core services (keep data)
  neutree-cli cleanup neutree-core

  # Remove obs-stack services and all data
  neutree-cli cleanup obs-stack --remove-data

  # Skip confirmation (for automation)
  neutree-cli cleanup neutree-core --force`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCleanup(&command.OSExecutor{}, opts, args[0])
		},
	}

	cmd.Flags().BoolVar(&opts.removeData, "remove-data", false, "Also remove persistent data (Docker volumes)")
	cmd.Flags().BoolVar(&opts.force, "force", false, "Skip confirmation prompt")

	return cmd
}

func runCleanup(executor command.Executor, opts *cleanupOptions, component string) error {
	if !validComponents[component] {
		return fmt.Errorf("invalid component %q: must be one of: neutree-core, obs-stack", component)
	}

	workDir := launch.LaunchWorkDir()
	composeFile := filepath.Join(workDir, component, "docker-compose.yml")

	if _, err := os.Stat(composeFile); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("compose file not found at %s: has %q been launched?", composeFile, component)
		}

		return errors.Wrapf(err, "failed to stat compose file at %s", composeFile)
	}

	if !opts.force {
		msg := fmt.Sprintf("This will stop and remove all %s containers.", component)
		if opts.removeData {
			msg += " All persistent data (volumes) will also be DELETED."
		}

		fmt.Println(msg)

		if !confirmPrompt("Are you sure you want to continue?") {
			fmt.Println("Aborted.")
			return nil
		}
	}

	args := []string{"compose", "-p", component, "-f", composeFile, "down"}
	if opts.removeData {
		args = append(args, "-v")
	}

	output, err := executor.Execute(context.Background(), "docker", args)
	if err != nil {
		return errors.Wrapf(err, "failed to run docker compose down for %s, output: %s", component, string(output))
	}

	fmt.Printf("%s has been cleaned up successfully.\n", component)

	return nil
}

// confirmPrompt shows a Y/n prompt and returns true if the user confirms.
func confirmPrompt(question string) bool {
	fmt.Printf("%s [y/N]: ", question)

	reader := bufio.NewReader(os.Stdin)

	answer, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	answer = strings.TrimSpace(strings.ToLower(answer))

	return answer == "y" || answer == "yes"
}
