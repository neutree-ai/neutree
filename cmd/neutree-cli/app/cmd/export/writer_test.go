package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/client"
)

func intPtr(n int) *int { return &n }

func TestJSONLWriter(t *testing.T) {
	var buf bytes.Buffer

	w, err := newTraceWriter("jsonl", &buf)
	require.NoError(t, err)
	require.NoError(t, w.Write(client.AITrace{RequestID: "a"}))
	require.NoError(t, w.Write(client.AITrace{RequestID: "b"}))
	require.NoError(t, w.Close())

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)

	var rec client.AITrace
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &rec))
	require.Equal(t, "b", rec.RequestID)
}

func TestJSONArrayWriterEmitsValidArray(t *testing.T) {
	var buf bytes.Buffer

	w, err := newTraceWriter("json", &buf)
	require.NoError(t, err)
	require.NoError(t, w.Write(client.AITrace{RequestID: "a"}))
	require.NoError(t, w.Write(client.AITrace{RequestID: "b"}))
	require.NoError(t, w.Close())

	var recs []client.AITrace
	require.NoError(t, json.Unmarshal(buf.Bytes(), &recs))
	require.Len(t, recs, 2)
}

func TestJSONArrayWriterEmptyIsValid(t *testing.T) {
	var buf bytes.Buffer

	w, err := newTraceWriter("json", &buf)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	var recs []client.AITrace
	require.NoError(t, json.Unmarshal(buf.Bytes(), &recs))
	require.Empty(t, recs)
}

func TestCSVWriterHeaderAndPointerColumns(t *testing.T) {
	var buf bytes.Buffer

	w, err := newTraceWriter("csv", &buf)
	require.NoError(t, err)
	require.NoError(t, w.Write(client.AITrace{
		RequestID:      "a",
		ResponseStatus: 200,
		TotalTokens:    intPtr(42),
		// PromptTokens left nil -> empty column
	}))
	require.NoError(t, w.Close())

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, strings.Join(csvHeader, ","), lines[0])
	require.Contains(t, lines[1], "a,")
	require.Contains(t, lines[1], ",42,") // total_tokens rendered
}

func TestUnsupportedFormat(t *testing.T) {
	_, err := newTraceWriter("xml", &bytes.Buffer{})
	require.Error(t, err)
}

func i64Ptr(n int64) *int64 { return &n }

func TestUsageCSVWriterHeaderAndNullColumns(t *testing.T) {
	var buf bytes.Buffer

	w, err := newUsageWriter("csv", &buf)
	require.NoError(t, err)
	require.NoError(t, w.Write(client.UsageRow{
		Date:       "2026-07-15",
		APIKeyName: "my-key",
		Usage:      i64Ptr(42),
		// token and cost columns left nil -> empty columns
	}))
	require.NoError(t, w.Close())

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, strings.Join(usageCSVHeader, ","), lines[0])
	require.Contains(t, lines[1], "my-key")
	require.True(t, strings.HasSuffix(lines[1], ",42,,,,,,")) // usage=42, token and cost columns empty
}

func TestUsageCSVWriterBreakdownColumns(t *testing.T) {
	var buf bytes.Buffer

	cost := 0.0125

	w, err := newUsageWriter("csv", &buf)
	require.NoError(t, err)
	require.NoError(t, w.Write(client.UsageRow{
		Date:                "2026-07-15",
		Usage:               i64Ptr(30),
		PromptTokens:        i64Ptr(10),
		CompletionTokens:    i64Ptr(20),
		CacheReadTokens:     i64Ptr(4),
		CacheCreationTokens: i64Ptr(2),
		ReasoningTokens:     i64Ptr(5),
		CostUSD:             &cost,
	}))
	require.NoError(t, w.Close())

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	require.True(t, strings.HasSuffix(lines[1], ",30,10,20,4,2,5,0.0125"), lines[1])
}

func TestUsageJSONWriterEmitsValidArray(t *testing.T) {
	var buf bytes.Buffer

	w, err := newUsageWriter("json", &buf)
	require.NoError(t, err)
	require.NoError(t, w.Write(client.UsageRow{APIKeyID: "a"}))
	require.NoError(t, w.Write(client.UsageRow{APIKeyID: "b"}))
	require.NoError(t, w.Close())

	var recs []client.UsageRow
	require.NoError(t, json.Unmarshal(buf.Bytes(), &recs))
	require.Len(t, recs, 2)
}

func TestUsageUnsupportedFormat(t *testing.T) {
	_, err := newUsageWriter("xml", &bytes.Buffer{})
	require.Error(t, err)
}

func TestCSVRoutingEvidence(t *testing.T) {
	var buf bytes.Buffer
	w, err := newTraceWriter("csv", &buf)
	require.NoError(t, err)
	routing := &client.TraceRouting{Result: "selected", Reason: "capacity_filtered", Selected: &client.TraceRoutingTarget{Upstream: "provider", UpstreamModel: "model"}, SkippedTotal: 1, Skipped: []client.TraceRoutingTarget{{Upstream: "full", Reason: "capacity_exhausted"}}}
	require.NoError(t, w.Write(client.AITrace{RequestID: "a", Routing: routing}))
	require.NoError(t, w.Close())
	rows, err := csv.NewReader(&buf).ReadAll()
	require.NoError(t, err)
	var decoded client.TraceRouting
	require.NoError(t, json.Unmarshal([]byte(rows[1][len(csvHeader)-1]), &decoded))
	require.Equal(t, *routing, decoded)
}
