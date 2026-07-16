package export

import (
	"bytes"
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
		// PromptTokens/CompletionTokens left nil -> empty columns
	}))
	require.NoError(t, w.Close())

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, strings.Join(usageCSVHeader, ","), lines[0])
	require.Contains(t, lines[1], "my-key")
	require.True(t, strings.HasSuffix(lines[1], ",42,,")) // usage=42, prompt/completion empty
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
