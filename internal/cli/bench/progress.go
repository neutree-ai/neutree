package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
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

	summary, err := decodeBenchmarkResultSummary(json.NewDecoder(file))
	if err != nil {
		return benchmarkResultSummary{}, fmt.Errorf("decode benchmark result: %w", err)
	}

	return summary, nil
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

// Progress can reach shared terminal or CI logs, unlike the local command.sh.
func progressBenchmarkCaseCommand(target BenchTarget, options Options, benchmarkCase Case, remoteRoot string) string {
	target.BaseURL = redactProgressCommandValue(target.BaseURL)
	target.Model = redactProgressCommandValue(target.Model)
	target.Tokenizer = redactProgressCommandValue(target.Tokenizer)

	return benchmarkCaseCommand(target, options, benchmarkCase, remoteRoot)
}

func redactProgressCommandValue(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return "<redacted>"
	}

	if parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" {
		return value
	}

	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""

	return parsed.String()
}
