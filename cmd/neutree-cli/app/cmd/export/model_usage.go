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

// usageLister fetches model-usage aggregates. *client.UsageService satisfies
// it; injecting the interface (rather than a concrete client) lets the export
// logic be unit tested with an in-memory fake — no HTTP server, no filesystem.
type usageLister interface {
	GetUsageByDimension(filters client.UsageFilters) ([]client.UsageRow, error)
}

// dateLayout is the YYYY-MM-DD form used by the RPC's DATE parameters and by the
// --since/--until flags.
const dateLayout = "2006-01-02"

// defaultWindowDays is how far back --since defaults when unset. It mirrors the
// UI's default usage window so the CLI and UI show the same range out of the box.
const defaultWindowDays = 30

// modelUsageOptions holds the flags for `export model-usage`.
type modelUsageOptions struct {
	workspace     string
	allWorkspaces bool
	format        string
	file          string

	since string
	until string

	apiKeyID     string
	endpoint     string
	model        string // client-side filter (RPC has no model param)
	endpointType string // client-side filter (RPC has no endpoint_type param)
}

func newModelUsageCmd() *cobra.Command {
	opts := &modelUsageOptions{}

	cmd := &cobra.Command{
		Use:   "model-usage",
		Short: "Export model usage statistics",
		Long: `Export model usage statistics (per day, API key, endpoint, and model).

Usage is already day-aggregated server-side, so the whole result set comes back
in a single request — there is no pagination. Data is written to stdout by
default (redirect with --file); all progress and diagnostics go to stderr, so
the data stream stays clean for pipes and redirection.

The default output is CSV (the common case is a spreadsheet); pass --format json
or jsonl for machine consumption. The default window is the last 30 days; bound
it with --since/--until (YYYY-MM-DD, both inclusive).

Pass --all-workspaces (-A) to aggregate across every workspace you may read.
--model and --endpoint-type filter client-side (the RPC does not take them),
matching the UI's behavior.

Examples:
  # Last 30 days for the default workspace, as CSV to stdout
  neutree-cli export model-usage -w default

  # A bounded window, all workspaces, to a file
  neutree-cli export model-usage -A --since 2026-07-01 --until 2026-07-15 -f usage.csv

  # One API key, JSON Lines piped to jq
  neutree-cli export model-usage --api-key-id <uuid> --format jsonl | jq -c .`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelUsageExport(opts)
		},
	}

	f := cmd.Flags()
	f.StringVarP(&opts.workspace, "workspace", "w", "default", "Workspace name")
	f.BoolVarP(&opts.allWorkspaces, "all-workspaces", "A", false, "Aggregate across every workspace you may read (mutually exclusive with --workspace)")
	f.StringVar(&opts.format, "format", "csv", "Output format: csv, json, jsonl")
	f.StringVarP(&opts.file, "file", "f", "", "Output file path (default: stdout)")
	f.StringVar(&opts.since, "since", "", "Start date, inclusive (YYYY-MM-DD; default: 30 days ago)")
	f.StringVar(&opts.until, "until", "", "End date, inclusive (YYYY-MM-DD; default: today)")
	f.StringVar(&opts.apiKeyID, "api-key-id", "", "Filter by API key ID (UUID)")
	f.StringVar(&opts.endpoint, "endpoint", "", "Filter by endpoint name")
	f.StringVar(&opts.model, "model", "", "Filter by model name (client-side)")
	f.StringVar(&opts.endpointType, "endpoint-type", "", "Filter by endpoint type (client-side)")

	cmd.MarkFlagsMutuallyExclusive("workspace", "all-workspaces")

	return cmd
}

func runModelUsageExport(opts *modelUsageOptions) error {
	c, err := global.NewClient()
	if err != nil {
		return err
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

	total, err := runUsageExport(usageExportRequest{
		lister:   c.Usage,
		dataOut:  bw,
		progress: os.Stderr,
		now:      time.Now(),
		opts:     opts,
	})
	if err != nil {
		return err
	}

	if err := bw.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "done: exported %d model usage record(s)\n", total)

	return nil
}

// usageExportRequest carries the fully-resolved inputs for a single export run.
// It depends only on the usageLister interface and io.Writers (plus an injected
// clock), so runUsageExport is unit testable with an in-memory lister and byte
// buffers.
type usageExportRequest struct {
	lister   usageLister
	dataOut  io.Writer // record stream (stdout or a file)
	progress io.Writer // diagnostics (stderr)
	now      time.Time // clock for the default window
	opts     *modelUsageOptions
}

// runUsageExport resolves the window, fetches the aggregates in one RPC, applies
// client-side filters, and writes each row in the chosen format. It returns the
// number of records written.
func runUsageExport(req usageExportRequest) (int, error) {
	start, end, err := resolveWindow(req.opts.since, req.opts.until, req.now)
	if err != nil {
		return 0, err
	}

	writer, err := newUsageWriter(req.opts.format, req.dataOut)
	if err != nil {
		return 0, err
	}

	rows, err := req.lister.GetUsageByDimension(client.UsageFilters{
		StartDate:    start,
		EndDate:      end,
		APIKeyID:     req.opts.apiKeyID,
		EndpointName: req.opts.endpoint,
		Workspace:    resolveUsageWorkspace(req.opts.workspace, req.opts.allWorkspaces),
	})
	if err != nil {
		return 0, err
	}

	total := 0

	for i := range rows {
		if !matchesClientFilters(rows[i], req.opts.model, req.opts.endpointType) {
			continue
		}

		if err := writer.Write(rows[i]); err != nil {
			return total, err
		}

		total++
	}

	if err := writer.Close(); err != nil {
		return total, err
	}

	fmt.Fprintf(req.progress, "exported %d model usage record(s)...\n", total)

	return total, nil
}

// resolveWindow fills in the default window (last defaultWindowDays through
// today, UTC) for any unset bound and validates explicit ones. Both bounds are
// inclusive dates, matching the RPC's BETWEEN semantics.
func resolveWindow(since, until string, now time.Time) (string, string, error) {
	end := until
	if end == "" {
		end = now.UTC().Format(dateLayout)
	} else if err := validateDate(end); err != nil {
		return "", "", fmt.Errorf("invalid --until: %w", err)
	}

	start := since
	if start == "" {
		start = now.UTC().AddDate(0, 0, -defaultWindowDays).Format(dateLayout)
	} else if err := validateDate(start); err != nil {
		return "", "", fmt.Errorf("invalid --since: %w", err)
	}

	return start, end, nil
}

// validateDate rejects anything that is not a bare YYYY-MM-DD date, so a
// malformed flag fails locally with a clear message instead of as a server error.
func validateDate(v string) error {
	if _, err := time.Parse(dateLayout, v); err != nil {
		return fmt.Errorf("%q is not a valid date (want YYYY-MM-DD)", v)
	}

	return nil
}

// resolveUsageWorkspace maps the workspace flags to the p_workspace value.
// --all-workspaces resolves to the empty string, which the client omits so the
// RPC sees SQL NULL (union across every workspace the caller may read) — no
// sentinel value, unlike the trace endpoint.
func resolveUsageWorkspace(workspace string, allWorkspaces bool) string {
	if allWorkspaces {
		return ""
	}

	return workspace
}

// matchesClientFilters applies the --model and --endpoint-type filters that the
// RPC does not support, mirroring the UI's client-side filtering. An empty
// filter matches everything.
func matchesClientFilters(row client.UsageRow, model, endpointType string) bool {
	if model != "" && row.ModelName != model {
		return false
	}

	if endpointType != "" && row.EndpointType != endpointType {
		return false
	}

	return true
}
