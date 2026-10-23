package bench

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewBenchCmdRegistersEngineFlags(t *testing.T) {
	cmd := NewBenchCmd()
	require.Equal(t, "bench ENDPOINT", cmd.Use)

	for _, flag := range []string{
		"engine", "workspace", "input-lens", "output-lens", "concurrencies",
		"num-prompts", "request-rate", "output-dir", "timeout",
	} {
		require.NotNil(t, cmd.Flags().Lookup(flag), flag)
	}
}

func TestBenchCommandRejectsUnsupportedEngineBeforeAPIClient(t *testing.T) {
	cmd := NewBenchCmd()
	cmd.SetArgs([]string{"--engine", "sglang", "chat"})

	err := cmd.Execute()
	require.ErrorContains(t, err, "unsupported engine")
	require.NotContains(t, err.Error(), "server URL is required")
}

func TestVLLMCommandValidatesOptionsBeforeCreatingAPIClient(t *testing.T) {
	cmd := NewBenchCmd()
	cmd.SetArgs([]string{"--engine", "vllm", "chat", "--request-rate", "0"})
	cmd.SetOut(&bytes.Buffer{})

	err := cmd.Execute()
	require.ErrorContains(t, err, "request-rate")
	require.NotContains(t, err.Error(), "server URL is required")
}
