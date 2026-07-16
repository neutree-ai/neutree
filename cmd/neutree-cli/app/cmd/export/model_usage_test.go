package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/client"
)

// fakeUsageLister is an in-memory usageLister. It records the filters it was
// called with (so tests can assert window/workspace resolution) and returns a
// canned set of rows.
type fakeUsageLister struct {
	rows    []client.UsageRow
	lastArg client.UsageFilters
	calls   int
	err     error
}

func (f *fakeUsageLister) GetUsageByDimension(filters client.UsageFilters) ([]client.UsageRow, error) {
	f.calls++
	f.lastArg = filters

	if f.err != nil {
		return nil, f.err
	}

	return f.rows, nil
}

func i64(n int64) *int64 { return &n }

func usageRow(model, endpointType string) client.UsageRow {
	return client.UsageRow{
		Date:             "2026-07-15",
		APIKeyID:         "key-1",
		APIKeyName:       "my-key",
		EndpointType:     endpointType,
		EndpointName:     "ep-1",
		ModelName:        model,
		Workspace:        "default",
		Usage:            i64(3),
		PromptTokens:     i64(10),
		CompletionTokens: i64(20),
	}
}

// fixedNow is a stable clock so default-window assertions are deterministic.
var fixedNow = time.Date(2026, 7, 16, 8, 30, 0, 0, time.UTC)

func runUsage(t *testing.T, lister usageLister, opts *modelUsageOptions) (int, string) {
	t.Helper()

	var data bytes.Buffer

	total, err := runUsageExport(usageExportRequest{
		lister:   lister,
		dataOut:  &data,
		progress: io.Discard,
		now:      fixedNow,
		opts:     opts,
	})
	require.NoError(t, err)

	return total, data.String()
}

func TestRunUsageExportDefaultWindowAndCSV(t *testing.T) {
	lister := &fakeUsageLister{rows: []client.UsageRow{usageRow("m1", "endpoint")}}

	total, out := runUsage(t, lister, &modelUsageOptions{format: "csv", workspace: "default"})

	require.Equal(t, 1, total)

	// Default window: today and today-30d, both inclusive.
	require.Equal(t, "2026-07-16", lister.lastArg.EndDate)
	require.Equal(t, "2026-06-16", lister.lastArg.StartDate)
	require.Equal(t, "default", lister.lastArg.Workspace)

	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 2) // header + 1 row
	require.Equal(t, strings.Join(usageCSVHeader, ","), lines[0])
	require.Contains(t, lines[1], "my-key")
}

func TestRunUsageExportExplicitWindow(t *testing.T) {
	lister := &fakeUsageLister{rows: []client.UsageRow{usageRow("m1", "endpoint")}}

	_, _ = runUsage(t, lister, &modelUsageOptions{
		format: "csv", workspace: "default",
		since: "2026-07-01", until: "2026-07-10",
	})

	require.Equal(t, "2026-07-01", lister.lastArg.StartDate)
	require.Equal(t, "2026-07-10", lister.lastArg.EndDate)
}

func TestRunUsageExportAllWorkspacesOmitsWorkspace(t *testing.T) {
	lister := &fakeUsageLister{rows: []client.UsageRow{usageRow("m1", "endpoint")}}

	_, _ = runUsage(t, lister, &modelUsageOptions{
		format: "csv", workspace: "default", allWorkspaces: true,
	})

	// All-workspaces resolves to empty so the client sends no p_workspace (NULL).
	require.Equal(t, "", lister.lastArg.Workspace)
}

func TestRunUsageExportClientSideFilters(t *testing.T) {
	lister := &fakeUsageLister{rows: []client.UsageRow{
		usageRow("gpt", "endpoint"),
		usageRow("llama", "external-endpoint"),
		usageRow("gpt", "external-endpoint"),
	}}

	// Filter to model=gpt AND endpoint-type=endpoint -> only the first row.
	total, out := runUsage(t, lister, &modelUsageOptions{
		format: "jsonl", workspace: "default",
		model: "gpt", endpointType: "endpoint",
	})

	require.Equal(t, 1, total)

	var rec client.UsageRow
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &rec))
	require.Equal(t, "gpt", rec.ModelName)
	require.Equal(t, "endpoint", rec.EndpointType)
}

func TestRunUsageExportRejectsBadDate(t *testing.T) {
	lister := &fakeUsageLister{}

	_, err := runUsageExport(usageExportRequest{
		lister:   lister,
		dataOut:  io.Discard,
		progress: io.Discard,
		now:      fixedNow,
		opts:     &modelUsageOptions{format: "csv", since: "07-01-2026"},
	})
	require.ErrorContains(t, err, "invalid --since")
	require.Equal(t, 0, lister.calls) // never reached the RPC
}

func TestRunUsageExportPropagatesListError(t *testing.T) {
	lister := &fakeUsageLister{err: fmt.Errorf("boom")}

	_, err := runUsageExport(usageExportRequest{
		lister:   lister,
		dataOut:  io.Discard,
		progress: io.Discard,
		now:      fixedNow,
		opts:     &modelUsageOptions{format: "csv"},
	})
	require.ErrorContains(t, err, "boom")
}

func TestRunUsageExportUnsupportedFormat(t *testing.T) {
	_, err := runUsageExport(usageExportRequest{
		lister:   &fakeUsageLister{},
		dataOut:  io.Discard,
		progress: io.Discard,
		now:      fixedNow,
		opts:     &modelUsageOptions{format: "xml"},
	})
	require.Error(t, err)
}

func TestResolveWindow(t *testing.T) {
	// Both defaulted.
	start, end, err := resolveWindow("", "", fixedNow)
	require.NoError(t, err)
	require.Equal(t, "2026-06-16", start)
	require.Equal(t, "2026-07-16", end)

	// Explicit values pass through.
	start, end, err = resolveWindow("2026-01-01", "2026-01-31", fixedNow)
	require.NoError(t, err)
	require.Equal(t, "2026-01-01", start)
	require.Equal(t, "2026-01-31", end)

	// An explicit past --until still yields a full trailing window: --since
	// defaults relative to the resolved end, not to "now".
	start, end, err = resolveWindow("", "2026-01-31", fixedNow)
	require.NoError(t, err)
	require.Equal(t, "2026-01-01", start)
	require.Equal(t, "2026-01-31", end)

	// A start after the end is rejected rather than sent to the server.
	_, _, err = resolveWindow("2026-07-10", "2026-07-01", fixedNow)
	require.ErrorContains(t, err, "after")

	// Malformed bounds are rejected.
	_, _, err = resolveWindow("nope", "", fixedNow)
	require.ErrorContains(t, err, "invalid --since")

	_, _, err = resolveWindow("", "nope", fixedNow)
	require.ErrorContains(t, err, "invalid --until")
}

func TestResolveUsageWorkspace(t *testing.T) {
	require.Equal(t, "prod", resolveUsageWorkspace("prod", false))
	require.Equal(t, "", resolveUsageWorkspace("prod", true)) // -A wins, omits filter
	require.Equal(t, "default", resolveUsageWorkspace("default", false))
}

func TestMatchesClientFilters(t *testing.T) {
	row := usageRow("gpt", "endpoint")

	require.True(t, matchesClientFilters(row, "", ""))                   // no filter
	require.True(t, matchesClientFilters(row, "gpt", "endpoint"))        // both match
	require.False(t, matchesClientFilters(row, "llama", ""))             // model mismatch
	require.False(t, matchesClientFilters(row, "", "external-endpoint")) // type mismatch
}
