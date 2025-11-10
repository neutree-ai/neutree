package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/engine"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/launch"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/model"
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

	neutreeCliCmd.AddCommand(launch.NewLaunchCmd())
	neutreeCliCmd.AddCommand(model.NewModelCmd())
	neutreeCliCmd.AddCommand(engine.NewEngineCmd())

	return neutreeCliCmd
}

func Execute() {
	err := NewNeutreeCliCommand().Execute()
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}
