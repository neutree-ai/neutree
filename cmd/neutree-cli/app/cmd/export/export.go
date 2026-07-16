package export

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/pkg/client"
)

// traceLister fetches one page of traces. *client.TracesService satisfies it;
// injecting it (rather than a concrete client) lets the export logic be unit
// tested with an in-memory fake — no HTTP server, no filesystem.
type traceLister interface {
	ListPage(workspace string, filters client.TraceListFilters, before string, limit int, includeBody bool) ([]client.AITrace, string, error)
}

// NewExportCmd creates the `export` parent command.
func NewExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export data from Neutree",
		Long:  "Export data from Neutree for archival or offline analysis.",
	}

	cmd.AddCommand(newAccessLogCmd())
	cmd.AddCommand(newModelUsageCmd())

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

	since   string
	until   string
	timeout time.Duration
	filter  client.TraceListFilters
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
	f.DurationVar(&opts.timeout, "timeout", 0, "Per-request HTTP timeout (0 = no timeout); a body-carrying page can exceed a short timeout")
	f.StringVar(&opts.since, "since", "", "Only export records at or after this time (RFC3339, or YYYY-MM-DD = start of that day)")
	f.StringVar(&opts.until, "until", "", "Only export records before this time (RFC3339, or YYYY-MM-DD = through the end of that day)")
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

// normalizeTimeBound converts a bare YYYY-MM-DD date to an RFC3339 instant so
// the server (and VictoriaLogs behind it) reads it unambiguously. A date-only
// lower bound becomes the start of that UTC day; a date-only upper bound
// (endOfDay=true) becomes the start of the *next* UTC day, so `--until <date>`
// includes the whole day. Values already carrying a time, or any other form,
// are passed through unchanged.
func normalizeTimeBound(v string, endOfDay bool) string {
	if v == "" {
		return ""
	}

	d, err := time.Parse("2006-01-02", v)
	if err != nil {
		return v // not a bare date (RFC3339, unix, etc.) — leave as-is
	}

	if endOfDay {
		d = d.AddDate(0, 0, 1)
	}

	return d.UTC().Format(time.RFC3339)
}

// resolveWorkspace maps the workspace flags to the value sent on the wire.
// --all-workspaces is a boolean (never collides with a real workspace name);
// it resolves to the server's cross-workspace sentinel.
func resolveWorkspace(workspace string, allWorkspaces bool) string {
	if allWorkspaces {
		return client.AllWorkspaces
	}

	return workspace
}

// effectiveLimit applies the body-mode cap. Bodies are large, so a
// body-carrying export that did not explicitly set --limit is capped to avoid
// accidentally pulling an unbounded volume. Returns the limit to use and
// whether the cap was applied.
func effectiveLimit(withBody, limitSet bool, limit int) (int, bool) {
	if withBody && !limitSet {
		return withBodyDefaultLimit, true
	}

	return limit, false
}

// validateLimit rejects a negative --limit. 0 means unlimited; a negative value
// is meaningless and would otherwise slip past the with-body cap (which only
// applies when --limit is unset) and be treated as unlimited.
func validateLimit(limit int) error {
	if limit < 0 {
		return fmt.Errorf("--limit must be >= 0 (0 = no limit)")
	}

	return nil
}

func runAccessLogExport(cmd *cobra.Command, opts *accessLogOptions) error {
	if err := validateLimit(opts.limit); err != nil {
		return err
	}

	// Exports are long-running: a full page of inline bodies can exceed the
	// client's default 30s timeout and abort a partial export. Default to no
	// timeout; --timeout lets the user reinstate one.
	c, err := global.NewClient(client.WithTimeout(opts.timeout))
	if err != nil {
		return err
	}

	if limit, capped := effectiveLimit(opts.withBody, cmd.Flags().Changed("limit"), opts.limit); capped {
		opts.limit = limit
		fmt.Fprintf(os.Stderr,
			"note: limiting to %d records because --with-body is on; pass --limit N (or --limit 0) to change\n",
			limit)
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

	total, err := runExport(exportRequest{
		lister:    c.Traces,
		dataOut:   bw,
		progress:  os.Stderr,
		workspace: resolveWorkspace(opts.workspace, opts.allWorkspaces),
		opts:      opts,
	})
	if err != nil {
		return err
	}

	if err := bw.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "done: exported %d access log record(s)\n", total)

	return nil
}

// exportRequest carries the fully-resolved inputs for a single export run. It
// depends only on interfaces and io.Writers, so runExport is unit testable with
// an in-memory lister and byte buffers.
type exportRequest struct {
	lister    traceLister
	dataOut   io.Writer // record stream (stdout or a file)
	progress  io.Writer // diagnostics (stderr)
	workspace string
	opts      *accessLogOptions
}

// runExport builds the output writer, streams every page through it, and
// finalizes. It returns the number of records written.
func runExport(req exportRequest) (int, error) {
	writer, err := newTraceWriter(req.opts.format, req.dataOut)
	if err != nil {
		return 0, err
	}

	filters := req.opts.filter
	// A bare YYYY-MM-DD is normalized to an RFC3339 instant before being sent
	// on to VictoriaLogs, which otherwise reads a date as that day's 00:00 —
	// so an inclusive-looking `--until <date>` would drop the whole day.
	filters.Start = normalizeTimeBound(req.opts.since, false)
	filters.End = normalizeTimeBound(req.opts.until, true)

	total, err := exportLoop(req.lister, req.progress, req.workspace, req.opts, filters, writer)
	if err != nil {
		return total, err
	}

	if err := writer.Close(); err != nil {
		return total, err
	}

	return total, nil
}

// exportLoop pages through the trace store, deduplicating by request id and
// stopping on stall, limit, or exhaustion. It returns the number of records
// written.
func exportLoop(lister traceLister, progress io.Writer, workspace string, opts *accessLogOptions, filters client.TraceListFilters, writer traceWriter) (int, error) {
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
		items, nextBefore, err := lister.ListPage(workspace, filters, before, perPage, opts.withBody)
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

		fmt.Fprintf(progress, "exported %d access log record(s)...\n", total)

		// Termination: the server reports no more pages, the cursor stops
		// advancing, or the whole page was records we already wrote (a
		// timestamp-boundary stall caused by the inclusive cursor).
		if nextBefore == "" {
			break
		}

		if nextBefore == before {
			fmt.Fprintf(progress, "warning: pagination cursor stopped advancing at %s; stopping early\n", nextBefore)
			break
		}

		if newInPage == 0 {
			fmt.Fprintf(progress, "warning: pagination stalled on duplicate records at %s; stopping early\n", nextBefore)
			break
		}

		before = nextBefore
	}

	return total, nil
}
