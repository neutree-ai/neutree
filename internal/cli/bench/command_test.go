package bench

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildBenchCommandUsesExplicitModelAndTokenizer(t *testing.T) {
	command := buildBenchCommand(BenchTarget{
		BaseURL:   "http://router-service.ns.svc.cluster.local:8000/default/chat",
		Model:     "Qwen/Qwen3.8-27B-FP8",
		Tokenizer: tokenizerOptions{Path: "/models/a model"},
	}, Case{InputLen: 16384, OutputLen: 2048, Concurrency: 2}, 20, "inf", "/tmp/bench-case")

	require.Contains(t, command, "vllm bench serve")
	require.Contains(t, command, "--backend 'openai-chat'")
	require.Contains(t, command, "--base-url 'http://router-service.ns.svc.cluster.local:8000/default/chat'")
	require.Contains(t, command, "--endpoint '/v1/chat/completions'")
	require.Contains(t, command, "--dataset-name 'random'")
	require.Contains(t, command, "--model 'Qwen/Qwen3.8-27B-FP8'")
	require.Contains(t, command, "--tokenizer '/models/a model'")
	require.Contains(t, command, "--result-filename 'result.json'")
	// The tqdm bar is the only live progress vllm bench reports. Suppressing it
	// leaves the run silent until the final metrics table.
	require.NotContains(t, command, "--disable-tqdm")
}

func TestBuildBenchCommandPassesTokenizerOverrides(t *testing.T) {
	command := buildBenchCommand(BenchTarget{
		BaseURL: "http://router-service.ns.svc.cluster.local:8000/default/chat",
		Model:   "Qwen/Qwen3.8-27B-FP8",
		Tokenizer: tokenizerOptions{
			Path:            "/models/a model",
			Mode:            "slow",
			TrustRemoteCode: true,
		},
	}, Case{InputLen: 16, OutputLen: 4, Concurrency: 2}, 20, "inf", "/tmp/bench-case")

	require.Contains(t, command, "--tokenizer '/models/a model'")
	require.Contains(t, command, "--tokenizer-mode 'slow'")
	require.Contains(t, command, "--trust-remote-code")
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	quoted := shellQuote("model's path")
	require.Equal(t, "'model'\"'\"'s path'", quoted)
	require.False(t, strings.Contains(quoted, "$("))
}
