package bench

import (
	"strconv"
	"strings"
	"time"
)

const (
	benchmarkBackend           = "openai-chat"
	benchmarkEndpoint          = "/v1/chat/completions"
	benchmarkDataset           = "random"
	benchmarkPercentiles       = "50,90,95,99"
	benchmarkPercentileMetrics = "ttft,tpot,itl,e2el"
)

// tokenizerOptions carries the tokenizer settings the serving engine runs
// with. vllm bench reuses them so prompt token lengths match the server.
type tokenizerOptions struct {
	Path            string
	Mode            string
	TrustRemoteCode bool
}

// BenchTarget contains the target-specific information shared by every case.
// runner and cleanup are intentionally private so they cannot enter the local
// JSON manifest.
type BenchTarget struct {
	Platform  string
	RuntimeID string
	BaseURL   string
	Model     string
	Tokenizer tokenizerOptions

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
		"--tokenizer", shellQuote(target.Tokenizer.Path),
	}

	if target.Tokenizer.Mode != "" {
		args = append(args, "--tokenizer-mode", shellQuote(target.Tokenizer.Mode))
	}

	if target.Tokenizer.TrustRemoteCode {
		args = append(args, "--trust-remote-code")
	}

	// `exec` replaces the shell with the benchmark, so the monitor is the
	// benchmark's direct parent and reaps it. The result directory is created
	// by prepareBenchmarkControl before the benchmark starts.
	return "exec vllm bench serve " + strings.Join(args, " ")
}

func benchmarkPreflightCommand() string {
	return "vllm bench serve --help"
}

// benchmarkRenewCommand stamps the lease file. The monitor compares its mtime
// with its own clock, so hosts never have to agree on wall-clock time.
func benchmarkRenewCommand(renewPath string) string {
	return "touch -- " + shellQuote(renewPath)
}

func benchmarkCancelCommand(cancelPath string) string {
	return ": > " + shellQuote(cancelPath)
}

func benchmarkMonitorCommand(remoteDir, command string, lease time.Duration) string {
	args := []string{
		shellQuote(benchmarkRenewPath(remoteDir)),
		shellQuote(benchmarkCancelPath(remoteDir)),
		shellQuote(benchmarkLogPath(remoteDir)),
		shellQuote(remoteDir),
		strconv.Itoa(int(lease / time.Second)),
		shellQuote(command),
	}

	return "python3 -c " + shellQuote(benchmarkMonitorScript) + " " + strings.Join(args, " ")
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
