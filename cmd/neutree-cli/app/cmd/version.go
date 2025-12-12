package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/internal/version"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long:  `Print the version, git commit, build time, and other build information for neutree-cli`,
		Run: func(cmd *cobra.Command, args []string) {
			info := version.Get()
			fmt.Println(info.String())
		},
	}
}
