package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/client"
)

// fakeTraceStore serves a fixed, time-desc-sorted set of traces with an
// inclusive cursor and a small server-side page cap — reproducing the real
// endpoint's boundary behavior (the cursor record repeats on the next page).
type fakeTraceStore struct {
	traces  []client.AITrace // sorted by Time desc
	pageCap int
}

func (f *fakeTraceStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	before := r.URL.Query().Get("before")

	limit := f.pageCap
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 && n < limit {
			limit = n
		}
	}

	// Inclusive filter: Time <= before (mimics passing the cursor as `end`).
	var filtered []client.AITrace

	for _, t := range f.traces {
		if before == "" || t.Time <= before {
			filtered = append(filtered, t)
		}
	}

	page := filtered
	next := ""

	if len(filtered) > limit {
		page = filtered[:limit]
		next = page[len(page)-1].Time // inclusive cursor -> boundary repeats
	}

	// When include_body is requested the store returns bodies inline, mirroring
	// the real endpoint's ?include_body=true projection.
	if r.URL.Query().Get("include_body") == "true" {
		withBodies := make([]client.AITrace, len(page))
		for i, t := range page {
			t.RequestBody = "body-" + t.RequestID
			withBodies[i] = t
		}

		page = withBodies
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":       page,
		"next_before": next,
	})
}

func newTestClient(t *testing.T, store *fakeTraceStore) (*client.Client, func()) {
	t.Helper()

	server := httptest.NewServer(store)

	return client.NewClient(server.URL, client.WithAPIKey("k")), server.Close
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

func TestExportLoopPaginatesAndDeduplicates(t *testing.T) {
	store := &fakeTraceStore{traces: makeTraces(12), pageCap: 5}
	c, closeFn := newTestClient(t, store)
	defer closeFn()

	var buf bytes.Buffer

	writer, err := newTraceWriter("jsonl", &buf)
	require.NoError(t, err)

	total, err := exportLoop(c, "default", &accessLogOptions{}, client.TraceListFilters{}, writer)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	// All 12 unique records exported exactly once despite the inclusive-cursor
	// boundary repeats across the 3 pages.
	require.Equal(t, 12, total)

	seen := map[string]bool{}

	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec client.AITrace
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		require.False(t, seen[rec.RequestID], "duplicate %s", rec.RequestID)
		seen[rec.RequestID] = true
	}

	require.Len(t, seen, 12)
}

func TestExportLoopRespectsLimit(t *testing.T) {
	store := &fakeTraceStore{traces: makeTraces(100), pageCap: 500}
	c, closeFn := newTestClient(t, store)
	defer closeFn()

	writer, err := newTraceWriter("jsonl", &bytes.Buffer{})
	require.NoError(t, err)

	total, err := exportLoop(c, "default", &accessLogOptions{limit: 7}, client.TraceListFilters{}, writer)
	require.NoError(t, err)
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

	store := &fakeTraceStore{traces: traces, pageCap: 5}
	c, closeFn := newTestClient(t, store)
	defer closeFn()

	writer, err := newTraceWriter("jsonl", &bytes.Buffer{})
	require.NoError(t, err)

	total, err := exportLoop(c, "default", &accessLogOptions{}, client.TraceListFilters{}, writer)
	require.NoError(t, err)
	// Only the first page's worth is recoverable; the loop terminates.
	require.Equal(t, 5, total)
}

func TestExportLoopWithBodyIncludesBodiesInline(t *testing.T) {
	// With --with-body the loop asks for include_body=true and the store returns
	// bodies inline in the same page — no per-record detail request.
	store := &fakeTraceStore{traces: makeTraces(2), pageCap: 5}
	c, closeFn := newTestClient(t, store)
	defer closeFn()

	var buf bytes.Buffer

	writer, err := newTraceWriter("jsonl", &buf)
	require.NoError(t, err)

	total, err := exportLoop(c, "default", &accessLogOptions{withBody: true}, client.TraceListFilters{}, writer)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.Equal(t, 2, total)
	require.Contains(t, buf.String(), "body-r000")
}
