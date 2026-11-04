package bench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// benchmarkResultSummary is the small, stable subset of a vLLM result that
// is useful for progress output. The complete version-dependent result stays
// in result.json.
type benchmarkResultSummary struct {
	Completed         *int     `json:"completed"`
	Failed            *int     `json:"failed"`
	DurationSeconds   *float64 `json:"duration"`
	RequestThroughput *float64 `json:"request_throughput"`
	OutputThroughput  *float64 `json:"output_throughput"`
	P50TTFTMS         *float64 `json:"p50_ttft_ms"`
	MedianTTFTMS      *float64 `json:"median_ttft_ms"`
	P99TTFTMS         *float64 `json:"p99_ttft_ms"`
}

func readBenchmarkResultSummary(path string) (benchmarkResultSummary, error) {
	file, err := os.Open(path)
	if err != nil {
		return benchmarkResultSummary{}, fmt.Errorf("read benchmark result: %w", err)
	}
	defer file.Close() //nolint:errcheck

	summary, err := decodeBenchmarkResultSummary(json.NewDecoder(newNaNTolerantReader(file)))
	if err != nil {
		return benchmarkResultSummary{}, fmt.Errorf("decode benchmark result: %w", err)
	}

	return summary, nil
}

// newNaNTolerantReader turns the non-finite float literals Python's json.dump
// writes (NaN, Infinity, -Infinity) into null. vLLM reports a metric that
// collected no samples that way, and encoding/json refuses the whole document
// over a value the summary may not even read. Text inside strings is left
// untouched.
//
// It looks ahead with Peek, so the source must be able to deliver its bytes
// without waiting on a live stream: it is meant for the result file, not for
// exec output.
func newNaNTolerantReader(source io.Reader) io.Reader {
	return &nanTolerantReader{reader: bufio.NewReader(source)}
}

type nanTolerantReader struct {
	reader   *bufio.Reader
	pending  []byte
	inString bool
	escaped  bool
}

func (r *nanTolerantReader) Read(p []byte) (int, error) {
	n := 0

	for n < len(p) {
		if len(r.pending) > 0 {
			copied := copy(p[n:], r.pending)
			n += copied
			r.pending = r.pending[copied:]

			continue
		}

		value, err := r.reader.ReadByte()
		if err != nil {
			if n > 0 {
				return n, nil
			}

			return 0, err
		}

		if r.inString {
			p[n] = value
			n++

			switch {
			case r.escaped:
				r.escaped = false
			case value == '\\':
				r.escaped = true
			case value == '"':
				r.inString = false
			}

			continue
		}

		if remaining, ok := r.consumeNonFiniteLiteral(value); ok {
			if _, err := r.reader.Discard(remaining); err != nil {
				return n, err
			}

			r.pending = append(r.pending, "null"...)

			continue
		}

		if value == '"' {
			r.inString = true
		}

		p[n] = value
		n++
	}

	return n, nil
}

// consumeNonFiniteLiteral reports how many bytes after value belong to a
// non-finite literal the reader has just started.
func (r *nanTolerantReader) consumeNonFiniteLiteral(value byte) (int, bool) {
	var literal string

	switch value {
	case 'N':
		literal = "NaN"
	case 'I':
		literal = "Infinity"
	case '-':
		literal = "-Infinity"
	default:
		return 0, false
	}

	peeked, _ := r.reader.Peek(len(literal) - 1)
	if string(peeked) != literal[1:] {
		return 0, false
	}

	return len(literal) - 1, true
}

func decodeBenchmarkResultSummary(decoder *json.Decoder) (benchmarkResultSummary, error) {
	token, err := decoder.Token()
	if err != nil {
		return benchmarkResultSummary{}, err
	}

	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return benchmarkResultSummary{}, fmt.Errorf("benchmark result must be a JSON object")
	}

	var summary benchmarkResultSummary

	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return benchmarkResultSummary{}, err
		}

		field, ok := fieldToken.(string)
		if !ok {
			return benchmarkResultSummary{}, fmt.Errorf("benchmark result field name is not a string")
		}

		if err := decodeBenchmarkResultField(decoder, field, &summary); err != nil {
			return benchmarkResultSummary{}, err
		}
	}

	closingToken, err := decoder.Token()
	if err != nil {
		return benchmarkResultSummary{}, err
	}

	if closingDelimiter, ok := closingToken.(json.Delim); !ok || closingDelimiter != '}' {
		return benchmarkResultSummary{}, fmt.Errorf("expected JSON delimiter %q", '}')
	}

	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return benchmarkResultSummary{}, err
		}

		return benchmarkResultSummary{}, fmt.Errorf("unexpected trailing JSON token %v", token)
	}

	return summary, nil
}

func decodeBenchmarkResultField(decoder *json.Decoder, field string, summary *benchmarkResultSummary) error {
	switch field {
	case "completed":
		return decoder.Decode(&summary.Completed)
	case "failed":
		return decoder.Decode(&summary.Failed)
	case "duration":
		return decoder.Decode(&summary.DurationSeconds)
	case "request_throughput":
		return decoder.Decode(&summary.RequestThroughput)
	case "output_throughput":
		return decoder.Decode(&summary.OutputThroughput)
	case "p50_ttft_ms":
		return decoder.Decode(&summary.P50TTFTMS)
	case "median_ttft_ms":
		return decoder.Decode(&summary.MedianTTFTMS)
	case "p99_ttft_ms":
		return decoder.Decode(&summary.P99TTFTMS)
	default:
		return skipJSONValue(decoder)
	}
}

// skipJSONValue consumes a value token by token, keeping detailed per-request
// results out of the CLI process heap.
func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}

	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	var closingDelimiter json.Delim

	switch delimiter {
	case '{':
		closingDelimiter = '}'
	case '[':
		closingDelimiter = ']'
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}

	for decoder.More() {
		if delimiter == '{' {
			if _, err := decoder.Token(); err != nil {
				return err
			}
		}

		if err := skipJSONValue(decoder); err != nil {
			return err
		}
	}

	token, err = decoder.Token()
	if err != nil {
		return err
	}

	if token != closingDelimiter {
		return fmt.Errorf("expected JSON delimiter %q", closingDelimiter)
	}

	return nil
}

func (s benchmarkResultSummary) String() string {
	parts := make([]string, 0, 7)
	if s.Completed != nil {
		parts = append(parts, fmt.Sprintf("completed=%d", *s.Completed))
	}

	if s.Failed != nil {
		parts = append(parts, fmt.Sprintf("failed=%d", *s.Failed))
	}

	if s.DurationSeconds != nil {
		parts = append(parts, fmt.Sprintf("duration_s=%.2f", *s.DurationSeconds))
	}

	if s.RequestThroughput != nil {
		parts = append(parts, fmt.Sprintf("request_per_s=%.2f", *s.RequestThroughput))
	}

	if s.OutputThroughput != nil {
		parts = append(parts, fmt.Sprintf("output_tokens_per_s=%.2f", *s.OutputThroughput))
	}

	if s.P50TTFTMS != nil {
		parts = append(parts, fmt.Sprintf("p50_ttft_ms=%.2f", *s.P50TTFTMS))
	} else if s.MedianTTFTMS != nil {
		parts = append(parts, fmt.Sprintf("median_ttft_ms=%.2f", *s.MedianTTFTMS))
	}

	if s.P99TTFTMS != nil {
		parts = append(parts, fmt.Sprintf("p99_ttft_ms=%.2f", *s.P99TTFTMS))
	}

	return strings.Join(parts, " ")
}

func benchmarkResultSummaryText(path string) string {
	summary, err := readBenchmarkResultSummary(path)
	if err != nil {
		return ""
	}

	return summary.String()
}
