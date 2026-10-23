package bench

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	internalbench "github.com/neutree-ai/neutree/internal/cli/bench"
)

type benchOptions struct {
	engine        string
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
		Long: `Run vLLM random-dataset benchmark cases against a managed Endpoint.

The benchmark client runs in one selected Endpoint runtime, while requests use
the normal data-plane route: Kubernetes uses the cluster Router and SSH/static
clusters use Ray Serve. Results therefore measure the Endpoint route, not a
single inference replica.

This command requires the default Admin API key because it reads the target
Cluster's kubeconfig or SSH credential through the existing credentials API.
The command does not perform special Admin-role validation; insufficient
permissions are reported by the Neutree API. The benchmark generates traffic
against the live Endpoint.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBench(cmd, opts, args[0])
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.engine, "engine", "", "Inference engine (currently: vllm)")

	_ = cmd.MarkFlagRequired("engine")

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
	if !strings.EqualFold(strings.TrimSpace(raw.engine), "vllm") {
		return fmt.Errorf("unsupported engine %q; only vllm is supported", raw.engine)
	}

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
		Workspace:      raw.workspace,
		Endpoint:       endpoint,
		InputLens:      inputLens,
		OutputLens:     outputLens,
		Concurrencies:  concurrencies,
		NumPrompts:     raw.numPrompts,
		RequestRate:    raw.requestRate,
		OutputDir:      raw.outputDir,
		Timeout:        raw.timeout,
		ProgressWriter: cmd.OutOrStdout(),
	}
	if err := options.Validate(); err != nil {
		return err
	}

	api, err := global.NewClient()
	if err != nil {
		return err
	}

	output, runErr := internalbench.Run(cmd.Context(), api, options)
	if output != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "benchmark results: %s\n", output)
	}

	return runErr
}
