package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/client"
)

// fakeLister is an in-memory traceLister. It reproduces the real endpoint's
// inclusive cursor and server-side page cap (so a boundary record repeats on
// the next page), letting the export logic be tested without an HTTP server.
type fakeLister struct {
	traces      []client.AITrace // sorted by Time desc
	pageCap     int
	calls       int
	lastInclude bool
	err         error
}

func (f *fakeLister) ListPage(_ string, _ client.TraceListFilters, before string, limit int, includeBody bool) ([]client.AITrace, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}

	f.calls++
	f.lastInclude = includeBody

	// Inclusive filter: Time <= before (mimics passing the cursor as `end`).
	var filtered []client.AITrace

	for _, t := range f.traces {
		if before == "" || t.Time <= before {
			filtered = append(filtered, t)
		}
	}

	pageLimit := f.pageCap
	if limit > 0 && limit < pageLimit {
		pageLimit = limit
	}

	page := filtered
	next := ""

	if len(filtered) > pageLimit {
		page = filtered[:pageLimit]
		next = page[len(page)-1].Time // inclusive cursor -> boundary repeats
	}

	if includeBody {
		withBodies := make([]client.AITrace, len(page))
		for i, t := range page {
			t.RequestBody = "body-" + t.RequestID
			withBodies[i] = t
		}

		page = withBodies
	}

	return page, next, nil
}

func makeTraces(n int) []client.AITrace {
	traces := make([]client.AITrace, n)
	for i := range n {
		// Descending, zero-padded so string comparison matches recency order.
		traces[i] = client.AITrace{
			RequestID: fmt.Sprintf("r%03d", i),
			Time:      fmt.Sprintf("2026-07-14T00:00:%05d", n-i),
		}
	}

	return traces
}

// runLoop drives exportLoop with an in-memory lister and a jsonl writer,
// returning the record count and the raw output.
func runLoop(t *testing.T, lister traceLister, opts *accessLogOptions) (int, string) {
	t.Helper()

	var buf bytes.Buffer

	writer, err := newTraceWriter("jsonl", &buf)
	require.NoError(t, err)

	total, err := exportLoop(lister, io.Discard, "default", opts, client.TraceListFilters{}, writer)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	return total, buf.String()
}

func TestExportLoopPaginatesAndDeduplicates(t *testing.T) {
	lister := &fakeLister{traces: makeTraces(12), pageCap: 5}

	total, out := runLoop(t, lister, &accessLogOptions{})

	// All 12 unique records exported exactly once despite the inclusive-cursor
	// boundary repeats across the 3 pages.
	require.Equal(t, 12, total)
	require.Greater(t, lister.calls, 1) // actually paged

	seen := map[string]bool{}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec client.AITrace
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		require.False(t, seen[rec.RequestID], "duplicate %s", rec.RequestID)
		seen[rec.RequestID] = true
	}

	require.Len(t, seen, 12)
}

func TestExportLoopRespectsLimit(t *testing.T) {
	lister := &fakeLister{traces: makeTraces(100), pageCap: 500}

	total, _ := runLoop(t, lister, &accessLogOptions{limit: 7})
	require.Equal(t, 7, total)
}

func TestExportLoopTerminatesOnStall(t *testing.T) {
	// More same-timestamp records than a page holds: the inclusive cursor can
	// never advance past them. The loop must detect the stall and stop rather
	// than spin forever.
	traces := make([]client.AITrace, 8)
	for i := range traces {
		traces[i] = client.AITrace{RequestID: fmt.Sprintf("r%d", i), Time: "2026-07-14T00:00:00Z"}
	}

	lister := &fakeLister{traces: traces, pageCap: 5}

	total, _ := runLoop(t, lister, &accessLogOptions{})
	// Only the first page's worth is recoverable; the loop terminates.
	require.Equal(t, 5, total)
}

func TestExportLoopWithBodyIncludesBodiesInline(t *testing.T) {
	// With --with-body the loop asks for include_body=true and the lister returns
	// bodies inline in the same page — no per-record detail request.
	lister := &fakeLister{traces: makeTraces(2), pageCap: 5}

	total, out := runLoop(t, lister, &accessLogOptions{withBody: true})
	require.Equal(t, 2, total)
	require.True(t, lister.lastInclude)
	require.Contains(t, out, "body-r000")
}

func TestExportLoopPropagatesListError(t *testing.T) {
	lister := &fakeLister{err: fmt.Errorf("boom")}

	writer, err := newTraceWriter("jsonl", io.Discard)
	require.NoError(t, err)

	_, err = exportLoop(lister, io.Discard, "default", &accessLogOptions{}, client.TraceListFilters{}, writer)
	require.ErrorContains(t, err, "boom")
}

func TestResolveWorkspace(t *testing.T) {
	require.Equal(t, "prod", resolveWorkspace("prod", false))
	require.Equal(t, client.AllWorkspaces, resolveWorkspace("prod", true))
	require.Equal(t, "default", resolveWorkspace("default", false))
}

func TestValidateLimit(t *testing.T) {
	require.NoError(t, validateLimit(0))  // 0 = unlimited
	require.NoError(t, validateLimit(10)) // positive is fine
	require.Error(t, validateLimit(-1))   // negative must be rejected
}

func TestEffectiveLimit(t *testing.T) {
	cases := []struct {
		name       string
		withBody   bool
		limitSet   bool
		limit      int
		wantLimit  int
		wantCapped bool
	}{
		{"body, no explicit limit -> capped", true, false, 0, withBodyDefaultLimit, true},
		{"body, explicit limit -> respected", true, true, 50, 50, false},
		{"body, explicit unlimited -> respected", true, true, 0, 0, false},
		{"no body -> never capped", false, false, 0, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, capped := effectiveLimit(tc.withBody, tc.limitSet, tc.limit)
			require.Equal(t, tc.wantLimit, got)
			require.Equal(t, tc.wantCapped, capped)
		})
	}
}

func TestNormalizeTimeBound(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		endOfDay bool
		want     string
	}{
		{"empty", "", false, ""},
		{"date since -> start of day", "2026-07-07", false, "2026-07-07T00:00:00Z"},
		{"date until -> next day start", "2026-07-14", true, "2026-07-15T00:00:00Z"},
		{"rfc3339 passthrough", "2026-07-07T04:20:00Z", false, "2026-07-07T04:20:00Z"},
		{"non-date passthrough", "5m", false, "5m"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, normalizeTimeBound(tc.in, tc.endOfDay))
		})
	}
}

func TestRunExportCSVToBufferNormalizesUntil(t *testing.T) {
	// End-to-end through runExport with byte buffers only: writer selection,
	// the loop, and finalization — no HTTP server, no filesystem.
	lister := &fakeLister{traces: makeTraces(3), pageCap: 500}

	var data, progress bytes.Buffer

	total, err := runExport(exportRequest{
		lister:    lister,
		dataOut:   &data,
		progress:  &progress,
		workspace: "default",
		opts:      &accessLogOptions{format: "csv", until: "2026-07-14"},
	})
	require.NoError(t, err)
	require.Equal(t, 3, total)

	lines := strings.Split(strings.TrimSpace(data.String()), "\n")
	require.Len(t, lines, 4) // header + 3 rows
	require.Equal(t, strings.Join(csvHeader, ","), lines[0])
}

func TestRunExportUnsupportedFormat(t *testing.T) {
	_, err := runExport(exportRequest{
		lister:    &fakeLister{},
		dataOut:   io.Discard,
		progress:  io.Discard,
		workspace: "default",
		opts:      &accessLogOptions{format: "xml"},
	})
	require.Error(t, err)
}
