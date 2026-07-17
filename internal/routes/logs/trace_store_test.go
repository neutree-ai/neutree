package logs

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/internal/util"
)

// fakeVL is a minimal VictoriaLogs stand-in: it records every LogsQL query it
// receives and answers each with the next canned NDJSON response.
type fakeVL struct {
	queries   []string
	responses []string
}

func (f *fakeVL) server(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.queries = append(f.queries, r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/x-ndjson")

		if len(f.responses) > 0 {
			_, _ = w.Write([]byte(f.responses[0]))
			f.responses = f.responses[1:]
		}
	}))
}

func newTestStore(serverURL string) *traceStore {
	return newTraceStore(&Dependencies{
		AITraceStoreURL: serverURL,
		HTTPClient:      &util.DefaultHTTPClient{},
	})
}

// chunkLine renders one chunk record the way VictoriaLogs returns it (all
// values as strings); data is base64-encoded here for test readability.
func chunkLine(requestID, kind string, seq int, rawData string) string {
	rec := map[string]string{
		"request_id": requestID,
		"chunk_kind": kind,
		"chunk_seq":  fmt.Sprintf("%d", seq),
		"chunk_data": base64.StdEncoding.EncodeToString([]byte(rawData)),
	}
	b, _ := json.Marshal(rec)

	return string(b) + "\n"
}

func TestBaseQuery_ExcludesChunkRecords(t *testing.T) {
	// Every query the store issues must filter out chunk records; forgetting
	// this on any path would leak storage internals into lists and stats.
	fake := &fakeVL{responses: []string{"", "", "", ""}}
	server := fake.server(t)

	defer server.Close()

	store := newTestStore(server.URL)

	_, err := store.List(`workspace:="ws1"`, traceFilters{}, 10, false, timeWindow{})
	require.NoError(t, err)

	_, err = store.Get(`workspace:="ws1"`, "req-1")
	require.NoError(t, err)

	_, err = store.DayCounts(`workspace:="ws1"`, timeMustParse(t, "2026-07-01T00:00:00Z"), timeMustParse(t, "2026-07-02T00:00:00Z"))
	require.NoError(t, err)

	_, err = store.KeyStats(`workspace:="ws1"`, timeMustParse(t, "2026-07-01T00:00:00Z"))
	require.NoError(t, err)

	require.Len(t, fake.queries, 4)

	for i, q := range fake.queries {
		assert.Containsf(t, q, `NOT record_type:="chunk"`, "query %d: %s", i, q)
	}
}

func TestGet_ReassemblesChunkedBodies(t *testing.T) {
	// A chunked trace is returned with whole bodies, reassembled from its
	// chunk records — out-of-order chunk rows must not corrupt the result.
	parent := `{"request_id":"req-1","workspace":"ws1","body_chunked":"true",` +
		`"request_chunks":"2","response_chunks":"1","response_status":"200"}` + "\n"
	chunks := chunkLine("req-1", "response", 0, `{"answer":42}`) +
		chunkLine("req-1", "request", 1, `world`) + // out of order on purpose
		chunkLine("req-1", "request", 0, `hello `)

	fake := &fakeVL{responses: []string{parent, chunks}}
	server := fake.server(t)

	defer server.Close()

	store := newTestStore(server.URL)

	item, err := store.Get(`workspace:="ws1"`, "req-1")
	require.NoError(t, err)
	require.NotNil(t, item)

	assert.Equal(t, "hello world", item.RequestBody)
	assert.Equal(t, `{"answer":42}`, item.ResponseBody)
	assert.False(t, item.BodyIncomplete)
	assert.False(t, item.BodyTruncated)

	// The chunk fetch is scoped like any trace query but must NOT carry the
	// chunk exclusion — it selects exactly the excluded records.
	require.Len(t, fake.queries, 2)
	assert.Contains(t, fake.queries[1], `record_type:="chunk"`)
	assert.Contains(t, fake.queries[1], `request_id:in("req-1")`)
	assert.NotContains(t, fake.queries[1], "NOT record_type")
	assert.Contains(t, fake.queries[1], `workspace:="ws1"`, "chunk fetch must stay permission-scoped")
}

func TestGet_MissingChunkMarksIncomplete(t *testing.T) {
	// Chunk records have no transactional tie to the parent: a lost chunk
	// degrades the body to its longest decodable prefix instead of failing.
	parent := `{"request_id":"req-1","workspace":"ws1","body_chunked":"true",` +
		`"request_chunks":"3","response_chunks":"0","response_status":"200"}` + "\n"
	chunks := chunkLine("req-1", "request", 0, "part0.") +
		chunkLine("req-1", "request", 2, "part2.") // seq 1 lost

	fake := &fakeVL{responses: []string{parent, chunks}}
	server := fake.server(t)

	defer server.Close()

	store := newTestStore(server.URL)

	item, err := store.Get(`workspace:="ws1"`, "req-1")
	require.NoError(t, err)
	require.NotNil(t, item)

	// Only the contiguous prefix survives; splicing part0+part2 would forge a
	// body that never existed.
	assert.Equal(t, "part0.", item.RequestBody)
	assert.True(t, item.BodyIncomplete)
}

func TestGet_LegacyRecordUnchanged(t *testing.T) {
	// Pre-chunking records carry no chunk metadata: no second query is made
	// and the inline bodies pass through untouched.
	parent := `{"request_id":"req-1","workspace":"ws1","response_status":"200",` +
		`"request_body":"{\"q\":1}","response_body":"{\"a\":2}"}` + "\n"

	fake := &fakeVL{responses: []string{parent}}
	server := fake.server(t)

	defer server.Close()

	store := newTestStore(server.URL)

	item, err := store.Get(`workspace:="ws1"`, "req-1")
	require.NoError(t, err)
	require.NotNil(t, item)

	assert.Equal(t, `{"q":1}`, item.RequestBody)
	assert.Equal(t, `{"a":2}`, item.ResponseBody)
	assert.False(t, item.BodyIncomplete)
	require.Len(t, fake.queries, 1, "no chunk fetch for a legacy record")
}

func TestList_IncludeBodyReassemblesInOneBatch(t *testing.T) {
	// A body-carrying page reassembles every chunked record on it with a
	// single batched chunk query; plain records are untouched.
	page := `{"request_id":"req-a","workspace":"ws1","body_chunked":"true",` +
		`"request_chunks":"1","response_chunks":"1","response_status":"200"}` + "\n" +
		`{"request_id":"req-b","workspace":"ws1","response_status":"200",` +
		`"request_body":"inline-req","response_body":"inline-resp"}` + "\n" +
		`{"request_id":"req-c","workspace":"ws1","body_chunked":"true",` +
		`"request_chunks":"1","response_chunks":"0","response_status":"500","body_truncated":"true"}` + "\n"
	chunks := chunkLine("req-a", "request", 0, "a-req") +
		chunkLine("req-a", "response", 0, "a-resp") +
		chunkLine("req-c", "request", 0, "c-req")

	fake := &fakeVL{responses: []string{page, chunks}}
	server := fake.server(t)

	defer server.Close()

	store := newTestStore(server.URL)

	items, err := store.List(`workspace:="ws1"`, traceFilters{}, 50, true, timeWindow{})
	require.NoError(t, err)
	require.Len(t, items, 3)

	assert.Equal(t, "a-req", items[0].RequestBody)
	assert.Equal(t, "a-resp", items[0].ResponseBody)
	assert.Equal(t, "inline-req", items[1].RequestBody)
	assert.Equal(t, "c-req", items[2].RequestBody)
	assert.Empty(t, items[2].ResponseBody)
	assert.True(t, items[2].BodyTruncated)
	assert.False(t, items[2].BodyIncomplete)

	require.Len(t, fake.queries, 2, "all chunked records share one chunk fetch")
	assert.Contains(t, fake.queries[1], `request_id:in("req-a","req-c")`)
	// The fetch limit is inferred from the parents' chunk counts (1+1 and
	// 1+0), not from any constant mirroring Vector's max_chunks.
	assert.Contains(t, fake.queries[1], "| limit 3")
	// Body-mode list queries must project the chunk metadata columns.
	assert.Contains(t, fake.queries[0], "body_chunked")
}

func TestList_WithoutBodySkipsChunkFetch(t *testing.T) {
	// Metadata-only pages never trigger chunk fetches, even when the page
	// contains chunked records (their chunk metadata is not even projected) —
	// but the body_truncated flag IS projected, so truncated traces stay
	// recognizable in the list view.
	page := `{"request_id":"req-a","workspace":"ws1","response_status":"200","body_truncated":"true"}` + "\n"

	fake := &fakeVL{responses: []string{page}}
	server := fake.server(t)

	defer server.Close()

	store := newTestStore(server.URL)

	items, err := store.List(`workspace:="ws1"`, traceFilters{}, 50, false, timeWindow{})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.True(t, items[0].BodyTruncated)
	require.Len(t, fake.queries, 1)
	assert.NotContains(t, fake.queries[0], "body_chunked")
	assert.Contains(t, fake.queries[0], "body_truncated")
}

func TestAssembleBody(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	cases := []struct {
		name   string
		parts  []traceChunk
		want   int
		body   string
		ok     bool
	}{
		{"empty complete", nil, 0, "", true},
		{"single", []traceChunk{{seq: 0, data: b64("abc")}}, 1, "abc", true},
		{"out of order", []traceChunk{{seq: 1, data: b64("def")}, {seq: 0, data: b64("abc")}}, 2, "abcdef", true},
		{"missing head", []traceChunk{{seq: 1, data: b64("def")}}, 2, "", false},
		{"missing middle keeps prefix", []traceChunk{{seq: 0, data: b64("abc")}, {seq: 2, data: b64("ghi")}}, 3, "abc", false},
		{"duplicate seq", []traceChunk{{seq: 0, data: b64("abc")}, {seq: 0, data: b64("abc")}}, 2, "abc", false},
		{"undecodable", []traceChunk{{seq: 0, data: "!!!not-base64!!!"}}, 1, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, ok := assembleBody(tc.parts, tc.want)
			assert.Equal(t, tc.body, body)
			assert.Equal(t, tc.ok, ok)
		})
	}
}

func TestVectorContract_ChunkedRoundTrip(t *testing.T) {
	// End-to-end shape check against the exact record layout Vector's
	// prepare_trace_payload emits for an oversized CJK body (verified against
	// vector 0.47.0): base64 chunk_data, integer-string chunk_seq, parent
	// counts. Byte-exactness across a chunk boundary is the property that
	// motivated base64 (raw chunks() splits multi-byte characters).
	body := strings.Repeat("访问日志超大请求体测试", 3) // multi-byte content
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	half := (len(encoded) / 2 / 4) * 4 // split on a 4-char base64 boundary mid-character

	parent := `{"request_id":"req-1","workspace":"ws1","body_chunked":"true",` +
		`"request_chunks":"2","response_chunks":"0","response_status":"200"}` + "\n"
	chunks := `{"request_id":"req-1","chunk_kind":"request","chunk_seq":"0","chunk_data":"` + encoded[:half] + `"}` + "\n" +
		`{"request_id":"req-1","chunk_kind":"request","chunk_seq":"1","chunk_data":"` + encoded[half:] + `"}` + "\n"

	fake := &fakeVL{responses: []string{parent, chunks}}
	server := fake.server(t)

	defer server.Close()

	store := newTestStore(server.URL)

	item, err := store.Get(`workspace:="ws1"`, "req-1")
	require.NoError(t, err)
	require.NotNil(t, item)
	assert.Equal(t, body, item.RequestBody)
	assert.False(t, item.BodyIncomplete)
}

func timeMustParse(t *testing.T, s string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)

	return parsed
}
