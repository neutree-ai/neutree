package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/launch"
)

func NewNeutreeCliCommand() *cobra.Command {
	neutreeCliCmd := &cobra.Command{
		Use:   "neutree-cli",
		Short: "Neutree Command Line Interface",
		Long: `Neutree CLI is the official command-line tool for managing and deploying Neutree platform components.

Available Commands:
  • launch: Deploy Neutree components and services

Examples:
  # Show help information
  neutree-cli --help
  
  # Deploy Neutree components
  neutree-cli launch [options]`,
	}

	neutreeCliCmd.AddCommand(launch.NewLaunchCmd())

	return neutreeCliCmd
}

func Execute() {
	err := NewNeutreeCliCommand().Execute()
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}
