package bench

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewBenchCmdRegistersFlags(t *testing.T) {
	cmd := NewBenchCmd()
	require.Equal(t, "bench ENDPOINT", cmd.Use)

	for _, flag := range []string{
		"workspace", "input-lens", "output-lens", "concurrencies",
		"num-prompts", "request-rate", "output-dir", "timeout",
	} {
		require.NotNil(t, cmd.Flags().Lookup(flag), flag)
	}
	require.Nil(t, cmd.Flags().Lookup("engine"))
}

func TestBenchCommandValidatesOptionsBeforeCreatingAPIClient(t *testing.T) {
	cmd := NewBenchCmd()
	cmd.SetArgs([]string{"chat", "--request-rate", "0"})
	cmd.SetOut(&bytes.Buffer{})

	err := cmd.Execute()
	require.ErrorContains(t, err, "request-rate")
	require.NotContains(t, err.Error(), "server URL is required")
}
