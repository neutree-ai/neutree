package bench

import (
	"strconv"
	"strings"
)

const (
	benchmarkBackend           = "openai-chat"
	benchmarkEndpoint          = "/v1/chat/completions"
	benchmarkDataset           = "random"
	benchmarkPercentiles       = "50,90,95,99"
	benchmarkPercentileMetrics = "ttft,tpot,itl,e2el"
)

// BenchTarget contains the target-specific information shared by every case.
// runner and cleanup are intentionally private so they cannot enter the local
// JSON manifest.
type BenchTarget struct {
	Platform  string
	RuntimeID string
	BaseURL   string
	Model     string
	Tokenizer string

	runner  remoteRunner
	cleanup func() error
}

func (t *BenchTarget) Close() error {
	if t == nil || t.cleanup == nil {
		return nil
	}

	return t.cleanup()
}

func buildBenchCommand(target BenchTarget, benchmarkCase Case, numPrompts int, requestRate, resultDir string) string {
	args := []string{
		"--backend", shellQuote(benchmarkBackend),
		"--base-url", shellQuote(target.BaseURL),
		"--endpoint", shellQuote(benchmarkEndpoint),
		"--model", shellQuote(target.Model),
		"--dataset-name", shellQuote(benchmarkDataset),
		"--random-input-len", shellQuote(strconv.Itoa(benchmarkCase.InputLen)),
		"--random-output-len", shellQuote(strconv.Itoa(benchmarkCase.OutputLen)),
		"--num-prompts", shellQuote(strconv.Itoa(numPrompts)),
		"--max-concurrency", shellQuote(strconv.Itoa(benchmarkCase.Concurrency)),
		"--request-rate", shellQuote(requestRate),
		"--save-result",
		"--result-dir", shellQuote(resultDir),
		"--result-filename", shellQuote("result.json"),
		"--save-detailed",
		"--metric-percentiles", shellQuote(benchmarkPercentiles),
		"--percentile-metrics", shellQuote(benchmarkPercentileMetrics),
		"--tokenizer", shellQuote(target.Tokenizer),
		"--disable-tqdm",
	}

	return "umask 077 && mkdir -p -- " + shellQuote(resultDir) + " && vllm bench serve " + strings.Join(args, " ")
}

func benchmarkPreflightCommand() string {
	return "vllm bench serve --help"
}

func remoteResultCommand(resultDir string) string {
	return "cat -- " + shellQuote(resultDir+"/result.json")
}

func remoteCleanupCommand(resultDir string) string {
	return "rm -rf -- " + shellQuote(resultDir)
}

// shellQuote produces one POSIX shell word. All remote commands use this
// helper rather than interpolating Endpoint, path, or model values directly.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
