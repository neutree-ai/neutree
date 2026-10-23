package exp

import (
	"github.com/spf13/cobra"

	benchcmd "github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/bench"
)

// NewExpCmd creates the experimental command group. Experimental features live
// behind this namespace so their CLI contracts can evolve without polluting the
// stable top-level command surface.
func NewExpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exp",
		Short: "Experimental commands",
	}
	cmd.AddCommand(benchcmd.NewBenchCmd())

	return cmd
}
