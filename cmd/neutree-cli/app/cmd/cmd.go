package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/apply"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/get"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/launch"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/model"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/packageimport"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/wait"
)

func NewNeutreeCliCommand() *cobra.Command {
	neutreeCliCmd := &cobra.Command{
		Use:   "neutree-cli",
		Short: "Neutree Command Line Interface",
		Long: `Neutree CLI is the official command-line tool for managing and deploying Neutree platform components.

Available Commands:
  • launch: Deploy Neutree components and services
  • model: Manage Neutree models
  • engine: Manage Neutree engines

Examples:
  # Show help information
  neutree-cli --help

  # Deploy Neutree components
  neutree-cli launch [options]

  # Manage Neutree models
  neutree-cli model [options]

  # Import an engine version package
  neutree-cli engine import --package vllm-v0.5.0.tar.gz --registry registry.example.com
	`,
	}

	global.AddFlags(neutreeCliCmd)
	neutreeCliCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		global.ResolveEnv()
	}

	neutreeCliCmd.AddCommand(apply.NewApplyCmd())
	neutreeCliCmd.AddCommand(get.NewGetCmd())
	neutreeCliCmd.AddCommand(launch.NewLaunchCmd())
	neutreeCliCmd.AddCommand(model.NewModelCmd())
	neutreeCliCmd.AddCommand(packageimport.NewImportCmd())
	neutreeCliCmd.AddCommand(wait.NewWaitCmd())
	neutreeCliCmd.AddCommand(newVersionCmd())

	return neutreeCliCmd
}

func Execute() {
	err := NewNeutreeCliCommand().Execute()
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}
