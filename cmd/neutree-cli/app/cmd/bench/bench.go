package bench

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	internalbench "github.com/neutree-ai/neutree/internal/cli/bench"
)

type benchOptions struct {
	workspace     string
	inputLens     []string
	outputLens    []string
	concurrencies []string
	numPrompts    int
	requestRate   string
	outputDir     string
	timeout       time.Duration
}

// NewBenchCmd creates the experimental benchmark command.
func NewBenchCmd() *cobra.Command {
	opts := &benchOptions{}
	cmd := &cobra.Command{
		Use:   "bench ENDPOINT",
		Short: "Run an experimental benchmark from a workload runtime",
		Long: `Run random-dataset benchmark cases against a managed Endpoint.

The benchmark client runs in one selected Endpoint runtime, while requests use
the normal data-plane route: Kubernetes uses the cluster Router and SSH/static
clusters use Ray Serve. Results therefore measure the Endpoint route, not a
single inference replica.

The command derives the engine from the Endpoint. This experimental release
supports vLLM Endpoints only.

This command requires the default Admin API key because it reads the target
Cluster's kubeconfig or SSH credential through the existing credentials API.
The command does not perform special Admin-role validation; insufficient
permissions are reported by the Neutree API. The benchmark generates traffic
against the live Endpoint.

Progress logs and the live benchmark log are written to stderr and recorded in
the results directory; the results directory is printed to stdout. The
benchmark's own progress bar streams to stderr as requests complete. SIGINT and
SIGTERM stop the current remote benchmark. The remote side also stops on its own
when the CLI stops renewing its lease, so a killed CLI does not leave a
benchmark running on the GPU.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBench(cmd, opts, args[0])
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&opts.workspace, "workspace", "w", "default", "Workspace containing the Endpoint")
	flags.StringSliceVar(&opts.inputLens, "input-lens", []string{"16384"}, "Input token lengths, comma-separated or repeated")
	flags.StringSliceVar(&opts.outputLens, "output-lens", []string{"2048"}, "Output token lengths, comma-separated or repeated")
	flags.StringSliceVar(&opts.concurrencies, "concurrencies", []string{"2"}, "Maximum request concurrencies, comma-separated or repeated")
	flags.IntVar(&opts.numPrompts, "num-prompts", 20, "Number of prompts per benchmark case")
	flags.StringVar(&opts.requestRate, "request-rate", "inf", "Request rate per second, or inf")
	flags.StringVarP(&opts.outputDir, "output-dir", "o", ".", "Directory on the CLI host in which to create the benchmark result directory")
	flags.DurationVar(&opts.timeout, "timeout", 0, "Overall benchmark timeout (0 = no timeout)")

	return cmd
}

func runBench(cmd *cobra.Command, raw *benchOptions, endpoint string) error {
	inputLens, err := internalbench.ParsePositiveIntegerList(raw.inputLens, "input-lens")
	if err != nil {
		return err
	}

	outputLens, err := internalbench.ParsePositiveIntegerList(raw.outputLens, "output-lens")
	if err != nil {
		return err
	}

	concurrencies, err := internalbench.ParsePositiveIntegerList(raw.concurrencies, "concurrencies")
	if err != nil {
		return err
	}

	options := internalbench.Options{
		Workspace:     raw.workspace,
		Endpoint:      endpoint,
		InputLens:     inputLens,
		OutputLens:    outputLens,
		Concurrencies: concurrencies,
		NumPrompts:    raw.numPrompts,
		RequestRate:   raw.requestRate,
		OutputDir:     raw.outputDir,
		Timeout:       raw.timeout,
	}
	if err := options.Validate(); err != nil {
		return err
	}

	api, err := global.NewClient()
	if err != nil {
		return err
	}

	ctx, stop := benchmarkContext(cmd.Context())
	defer stop()

	output, runErr := internalbench.Run(ctx, api, options)
	if output != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "benchmark results: %s\n", output)
	}

	return runErr
}

func benchmarkContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
