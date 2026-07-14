package export

import (
	"bufio"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/pkg/client"
)

// NewExportCmd creates the `export` parent command.
func NewExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export data from Neutree",
		Long:  "Export data from Neutree for archival or offline analysis.",
	}

	cmd.AddCommand(newAccessLogCmd())

	return cmd
}

// accessLogOptions holds the flags for `export access-log`.
type accessLogOptions struct {
	workspace     string
	allWorkspaces bool
	format        string
	file          string
	limit         int
	withBody      bool

	since  string
	until  string
	filter client.TraceListFilters
}

// withBodyDefaultLimit caps a body-carrying export that did not set an explicit
// --limit. Full request/response bodies are large, so an unbounded default
// would produce enormous output; users who really want everything pass an
// explicit --limit (0 for no cap).
const withBodyDefaultLimit = 2000

func newAccessLogCmd() *cobra.Command {
	opts := &accessLogOptions{}

	cmd := &cobra.Command{
		Use:   "access-log",
		Short: "Export access logs (AI traces)",
		Long: `Export access logs (AI inference traces) for a workspace.

Records are streamed page by page to the output, so exporting large histories
never buffers the full result set in memory. Data is written to stdout by
default (redirect with --file); all progress and diagnostics go to stderr, so
the data stream stays clean for pipes and redirection.

Full request/response bodies are included by default. Because bodies are large,
a body-carrying export without an explicit --limit is capped (pass --limit 0 to
lift the cap); use --with-body=false to export metadata only.

Use --since/--until to bound the time window for large exports. Pass
--all-workspaces (-A) to aggregate across every workspace you may read.

Examples:
  # Stream records (with bodies) as JSON Lines to stdout
  neutree-cli export access-log -w default

  # Metadata only, a time window, to a CSV file
  neutree-cli export access-log -w default --with-body=false \
    --since 2026-07-01 --until 2026-07-14 --format csv -f traces.csv

  # Aggregate across all workspaces, filter by status, pipe to jq
  neutree-cli export access-log -A --status 500 | jq -c .

  # Export everything for one API key over the last week
  neutree-cli export access-log -w default --api-key-id <uuid> --since 2026-07-07 --limit 0`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAccessLogExport(cmd, opts)
		},
	}

	f := cmd.Flags()
	f.StringVarP(&opts.workspace, "workspace", "w", "default", "Workspace name")
	f.BoolVarP(&opts.allWorkspaces, "all-workspaces", "A", false, "Aggregate across every workspace you may read (mutually exclusive with --workspace)")
	f.StringVar(&opts.format, "format", "jsonl", "Output format: jsonl, json, csv")
	f.StringVarP(&opts.file, "file", "f", "", "Output file path (default: stdout)")
	f.IntVar(&opts.limit, "limit", 0, "Maximum number of records to export (0 = no limit)")
	f.BoolVar(&opts.withBody, "with-body", true, "Include full request/response bodies")
	f.StringVar(&opts.since, "since", "", "Only export records at or after this time (RFC3339 or YYYY-MM-DD)")
	f.StringVar(&opts.until, "until", "", "Only export records before this time (RFC3339 or YYYY-MM-DD)")
	f.StringVar(&opts.filter.EndpointName, "endpoint", "", "Filter by endpoint name")
	f.StringVar(&opts.filter.EndpointType, "endpoint-type", "", "Filter by endpoint type (endpoint or external-endpoint)")
	f.StringVar(&opts.filter.Model, "model", "", "Filter by request or response model")
	f.StringVar(&opts.filter.Status, "status", "", "Filter by HTTP response status")
	f.StringVar(&opts.filter.APIKeyID, "api-key-id", "", "Filter by API key ID")
	f.StringVar(&opts.filter.FinishReason, "finish-reason", "", "Filter by finish reason")

	cmd.MarkFlagsMutuallyExclusive("workspace", "all-workspaces")

	return cmd
}

// perPageMax is the server's maximum page size.
const perPageMax = client.MaxTracePageSize

func runAccessLogExport(cmd *cobra.Command, opts *accessLogOptions) error {
	c, err := global.NewClient()
	if err != nil {
		return err
	}

	// --all-workspaces is exposed as a boolean so it never collides with a real
	// workspace name; the server's cross-workspace aggregate is requested via
	// the AllWorkspaces sentinel on the wire.
	workspace := opts.workspace
	if opts.allWorkspaces {
		workspace = client.AllWorkspaces
	}

	// Bodies are large: a body-carrying export that did not set --limit is
	// capped so a bare command cannot accidentally pull an unbounded volume.
	if opts.withBody && !cmd.Flags().Changed("limit") {
		opts.limit = withBodyDefaultLimit
		fmt.Fprintf(os.Stderr,
			"note: limiting to %d records because --with-body is on; pass --limit N (or --limit 0) to change\n",
			withBodyDefaultLimit)
	}

	// Resolve output. A file is buffered; stdout is written directly. Progress
	// always goes to stderr so it never corrupts the data stream.
	var out *os.File

	if opts.file != "" {
		out, err = os.Create(opts.file)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer out.Close()
	} else {
		out = os.Stdout
	}

	bw := bufio.NewWriter(out)
	defer bw.Flush() //nolint:errcheck

	writer, err := newTraceWriter(opts.format, bw)
	if err != nil {
		return err
	}

	filters := opts.filter
	filters.Start = opts.since
	filters.End = opts.until

	total, err := exportLoop(c, workspace, opts, filters, writer)
	if err != nil {
		return err
	}

	if err := writer.Close(); err != nil {
		return err
	}

	if err := bw.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "done: exported %d access log record(s)\n", total)

	return nil
}

// exportLoop pages through the trace store, deduplicating by request id and
// stopping on stall, limit, or exhaustion. It returns the number of records
// written.
func exportLoop(c *client.Client, workspace string, opts *accessLogOptions, filters client.TraceListFilters, writer traceWriter) (int, error) {
	seen := make(map[string]struct{})

	var (
		before string
		total  int
	)

	for {
		perPage := perPageMax
		if opts.limit > 0 && opts.limit-total < perPage {
			perPage = opts.limit - total
		}

		// Bodies come inline via include_body, so a full-content export is still
		// a single request per page — no per-record detail lookup.
		items, nextBefore, err := c.Traces.ListPage(workspace, filters, before, perPage, opts.withBody)
		if err != nil {
			return total, err
		}

		if len(items) == 0 {
			break
		}

		newInPage := 0

		for i := range items {
			t := items[i]

			if _, dup := seen[t.RequestID]; dup {
				continue
			}

			seen[t.RequestID] = struct{}{}
			newInPage++

			if err := writer.Write(t); err != nil {
				return total, err
			}

			total++

			if opts.limit > 0 && total >= opts.limit {
				return total, nil
			}
		}

		fmt.Fprintf(os.Stderr, "exported %d access log record(s)...\n", total)

		// Termination: the server reports no more pages, the cursor stops
		// advancing, or the whole page was records we already wrote (a
		// timestamp-boundary stall caused by the inclusive cursor).
		if nextBefore == "" {
			break
		}

		if nextBefore == before {
			fmt.Fprintf(os.Stderr, "warning: pagination cursor stopped advancing at %s; stopping early\n", nextBefore)
			break
		}

		if newInPage == 0 {
			fmt.Fprintf(os.Stderr, "warning: pagination stalled on duplicate records at %s; stopping early\n", nextBefore)
			break
		}

		before = nextBefore
	}

	return total, nil
}
