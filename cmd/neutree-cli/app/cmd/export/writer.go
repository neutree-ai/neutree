package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/neutree-ai/neutree/pkg/client"
)

// recordWriter streams records of type T to an output in a specific format.
// Records are written one at a time so exports never buffer the full result set
// in memory. Close finalizes the stream (e.g. the closing JSON bracket).
type recordWriter[T any] interface {
	Write(t T) error
	Close() error
}

// newRecordWriter builds a writer for the given format. Supported formats:
// "jsonl" (default), "json", "csv". header and csvRow describe how a record
// maps to CSV columns; JSON/JSONL formats marshal the record directly.
func newRecordWriter[T any](format string, w io.Writer, header []string, csvRow func(T) []string) (recordWriter[T], error) {
	switch format {
	case "jsonl", "":
		return &jsonlWriter[T]{enc: json.NewEncoder(w)}, nil
	case "json":
		return &jsonArrayWriter[T]{w: w}, nil
	case "csv":
		cw := &csvWriter[T]{cw: csv.NewWriter(w), row: csvRow}
		if err := cw.cw.Write(header); err != nil {
			return nil, err
		}

		return cw, nil
	default:
		return nil, fmt.Errorf("unsupported format %q (want jsonl, json, or csv)", format)
	}
}

// jsonlWriter emits one JSON object per line (newline-delimited JSON).
type jsonlWriter[T any] struct {
	enc *json.Encoder
}

func (jw *jsonlWriter[T]) Write(t T) error { return jw.enc.Encode(t) }
func (jw *jsonlWriter[T]) Close() error    { return nil }

// jsonArrayWriter emits a single well-formed JSON array, streamed element by
// element so the whole slice never lives in memory at once.
type jsonArrayWriter[T any] struct {
	w       io.Writer
	started bool
	failed  bool
}

func (aw *jsonArrayWriter[T]) Write(t T) error {
	sep := "[\n  "
	if aw.started {
		sep = ",\n  "
	}

	if _, err := io.WriteString(aw.w, sep); err != nil {
		aw.failed = true
		return err
	}

	aw.started = true

	b, err := json.Marshal(t)
	if err != nil {
		aw.failed = true
		return err
	}

	if _, err := aw.w.Write(b); err != nil {
		aw.failed = true
		return err
	}

	return nil
}

func (aw *jsonArrayWriter[T]) Close() error {
	if aw.failed {
		return nil
	}

	if !aw.started {
		_, err := io.WriteString(aw.w, "[]\n")
		return err
	}

	_, err := io.WriteString(aw.w, "\n]\n")

	return err
}

// csvWriter emits one CSV row per record, mapping a record to its columns with
// the row function supplied at construction.
type csvWriter[T any] struct {
	cw  *csv.Writer
	row func(T) []string
}

func (cw *csvWriter[T]) Write(t T) error { return cw.cw.Write(cw.row(t)) }

func (cw *csvWriter[T]) Close() error {
	cw.cw.Flush()
	return cw.cw.Error()
}

// --- access-log (AI trace) writer ---

// traceWriter is the access-log record writer.
type traceWriter = recordWriter[client.AITrace]

// csvHeader lists the trace columns in the order traceCSVRow emits them.
var csvHeader = []string{
	"request_id", "time", "workspace", "endpoint_type", "endpoint_name",
	"api_key_id", "request_uri", "request_model", "response_model",
	"response_status", "prompt_tokens", "completion_tokens", "total_tokens",
	"finish_reason", "stream", "user_agent", "duration_ms",
	"request_body", "response_body", "body_truncated", "body_incomplete",
}

// traceCSVRow renders a trace as a CSV row. Body columns are empty unless the
// export was run with --with-body.
func traceCSVRow(t client.AITrace) []string {
	return []string{
		t.RequestID, t.Time, t.Workspace, t.EndpointType, t.EndpointName,
		t.APIKeyID, t.RequestURI, t.RequestModel, t.ResponseModel,
		strconv.Itoa(t.ResponseStatus),
		intPtrStr(t.PromptTokens), intPtrStr(t.CompletionTokens), intPtrStr(t.TotalTokens),
		t.FinishReason, strconv.FormatBool(t.Stream), t.UserAgent, intPtrStr(t.DurationMs),
		t.RequestBody, t.ResponseBody,
		strconv.FormatBool(t.BodyTruncated), strconv.FormatBool(t.BodyIncomplete),
	}
}

// newTraceWriter builds an access-log writer for the given format.
func newTraceWriter(format string, w io.Writer) (traceWriter, error) {
	return newRecordWriter(format, w, csvHeader, traceCSVRow)
}

// intPtrStr renders a *int as its decimal string, or "" when nil.
func intPtrStr(p *int) string {
	if p == nil {
		return ""
	}

	return strconv.Itoa(*p)
}

// --- model-usage writer ---

// usageWriter is the model-usage record writer.
type usageWriter = recordWriter[client.UsageRow]

// usageCSVHeader lists the model-usage columns in the order usageCSVRow emits
// them; it matches the get_usage_by_dimension RPC return shape.
var usageCSVHeader = []string{
	"date", "api_key_id", "api_key_name", "endpoint_type", "endpoint_name",
	"model_name", "workspace", "usage", "prompt_tokens", "completion_tokens",
}

// usageCSVRow renders one usage aggregate bucket as a CSV row. Token columns are
// empty for pre-dimensional records the server returns with NULL counts.
func usageCSVRow(u client.UsageRow) []string {
	return []string{
		u.Date, u.APIKeyID, u.APIKeyName, u.EndpointType, u.EndpointName,
		u.ModelName, u.Workspace,
		int64PtrStr(u.Usage), int64PtrStr(u.PromptTokens), int64PtrStr(u.CompletionTokens),
	}
}

// newUsageWriter builds a model-usage writer for the given format.
func newUsageWriter(format string, w io.Writer) (usageWriter, error) {
	return newRecordWriter(format, w, usageCSVHeader, usageCSVRow)
}

// int64PtrStr renders a *int64 as its decimal string, or "" when nil.
func int64PtrStr(p *int64) string {
	if p == nil {
		return ""
	}

	return strconv.FormatInt(*p, 10)
}
