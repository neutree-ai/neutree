package bench

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadBenchmarkResultSummary(t *testing.T) {
	t.Run("formats available metrics", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), benchmarkResultFileName)
		require.NoError(t, os.WriteFile(path, []byte(`{
  "completed": 20,
  "failed": 0,
  "duration": 1.5,
  "request_throughput": 2.5,
  "output_throughput": 10.5,
  "p50_ttft_ms": 12.5,
  "p99_ttft_ms": 25.5
}`), 0600))

		summary, err := readBenchmarkResultSummary(path)
		require.NoError(t, err)
		require.Equal(
			t,
			"completed=20 failed=0 duration_s=1.50 request_per_s=2.50 output_tokens_per_s=10.50 p50_ttft_ms=12.50 p99_ttft_ms=25.50",
			summary.String(),
		)
	})

	t.Run("accepts a result without known summary fields", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), benchmarkResultFileName)
		require.NoError(t, os.WriteFile(path, []byte(`{"version":"future"}`), 0600))

		summary, err := readBenchmarkResultSummary(path)
		require.NoError(t, err)
		require.Empty(t, summary.String())
	})

	t.Run("falls back to the legacy TTFT median", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), benchmarkResultFileName)
		require.NoError(t, os.WriteFile(path, []byte(`{"median_ttft_ms":12.5}`), 0600))

		summary, err := readBenchmarkResultSummary(path)
		require.NoError(t, err)
		require.Equal(t, "median_ttft_ms=12.50", summary.String())
	})

	t.Run("skips detailed results", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), benchmarkResultFileName)
		require.NoError(t, os.WriteFile(path, []byte(`{
  "completed": 20,
  "detailed": [{
    "generated_text": "response",
    "request_timings": {"first_token": [1, 2, 3]}
  }],
  "p99_ttft_ms": 25.5
}`), 0600))

		summary, err := readBenchmarkResultSummary(path)
		require.NoError(t, err)
		require.Equal(t, "completed=20 p99_ttft_ms=25.50", summary.String())
	})

	t.Run("rejects malformed JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), benchmarkResultFileName)
		require.NoError(t, os.WriteFile(path, []byte(`{`), 0600))

		_, err := readBenchmarkResultSummary(path)
		require.ErrorContains(t, err, "decode benchmark result")
	})
}

func TestProgressBenchmarkCaseCommandRedactsSensitiveURLParts(t *testing.T) {
	target := BenchTarget{
		BaseURL:   "https://user:base-url-secret@router.example.test/endpoint?access_token=base-url-token#fragment",
		Model:     "https://models.example.test/qwen?token=model-token",
		Tokenizer: "/models/qwen?token=tokenizer-token",
	}
	options := Options{NumPrompts: 20, RequestRate: "inf"}
	benchmarkCase := Case{InputLen: 16, OutputLen: 4, Concurrency: 2}

	command := progressBenchmarkCaseCommand(target, options, benchmarkCase, "/tmp/neutree-bench-test")
	require.Contains(t, command, "--base-url 'https://router.example.test/endpoint'")
	require.Contains(t, command, "--model 'https://models.example.test/qwen'")
	require.Contains(t, command, "--tokenizer '/models/qwen'")
	require.NotContains(t, command, "base-url-secret")
	require.NotContains(t, command, "base-url-token")
	require.NotContains(t, command, "model-token")
	require.NotContains(t, command, "tokenizer-token")
}
