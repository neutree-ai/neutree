package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/neutree-ai/neutree/pkg/client"
)

// traceWriter streams AI trace records to an output in a specific format.
// Records are written one at a time so exports never buffer the full result
// set in memory. Close finalizes the stream (e.g. the closing JSON bracket).
type traceWriter interface {
	Write(t client.AITrace) error
	Close() error
}

// newTraceWriter builds a writer for the given format. Supported formats:
// "jsonl" (default), "json", "csv".
func newTraceWriter(format string, w io.Writer) (traceWriter, error) {
	switch format {
	case "jsonl", "":
		return &jsonlWriter{enc: json.NewEncoder(w)}, nil
	case "json":
		return &jsonArrayWriter{w: w}, nil
	case "csv":
		cw := &csvWriter{cw: csv.NewWriter(w)}
		if err := cw.cw.Write(csvHeader); err != nil {
			return nil, err
		}

		return cw, nil
	default:
		return nil, fmt.Errorf("unsupported format %q (want jsonl, json, or csv)", format)
	}
}

// jsonlWriter emits one JSON object per line (newline-delimited JSON).
type jsonlWriter struct {
	enc *json.Encoder
}

func (jw *jsonlWriter) Write(t client.AITrace) error { return jw.enc.Encode(t) }
func (jw *jsonlWriter) Close() error                 { return nil }

// jsonArrayWriter emits a single well-formed JSON array, streamed element by
// element so the whole slice never lives in memory at once.
type jsonArrayWriter struct {
	w       io.Writer
	started bool
	failed  bool
}

func (aw *jsonArrayWriter) Write(t client.AITrace) error {
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

func (aw *jsonArrayWriter) Close() error {
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

// csvHeader lists the columns in the order csvWriter emits them.
var csvHeader = []string{
	"request_id", "time", "workspace", "endpoint_type", "endpoint_name",
	"api_key_id", "request_uri", "request_model", "response_model",
	"response_status", "prompt_tokens", "completion_tokens", "total_tokens",
	"finish_reason", "stream", "user_agent", "duration_ms",
	"request_body", "response_body",
}

// csvWriter emits one CSV row per trace. Body columns are empty unless the
// export was run with --with-body.
type csvWriter struct {
	cw *csv.Writer
}

func (cw *csvWriter) Write(t client.AITrace) error {
	return cw.cw.Write([]string{
		t.RequestID, t.Time, t.Workspace, t.EndpointType, t.EndpointName,
		t.APIKeyID, t.RequestURI, t.RequestModel, t.ResponseModel,
		strconv.Itoa(t.ResponseStatus),
		intPtrStr(t.PromptTokens), intPtrStr(t.CompletionTokens), intPtrStr(t.TotalTokens),
		t.FinishReason, strconv.FormatBool(t.Stream), t.UserAgent, intPtrStr(t.DurationMs),
		t.RequestBody, t.ResponseBody,
	})
}

func (cw *csvWriter) Close() error {
	cw.cw.Flush()
	return cw.cw.Error()
}

// intPtrStr renders a *int as its decimal string, or "" when nil.
func intPtrStr(p *int) string {
	if p == nil {
		return ""
	}

	return strconv.Itoa(*p)
}
