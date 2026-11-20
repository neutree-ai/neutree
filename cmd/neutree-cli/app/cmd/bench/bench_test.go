package bench

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewBenchCmdRegistersFlags(t *testing.T) {
	cmd := NewBenchCmd()
	require.Equal(t, "bench ENDPOINT", cmd.Use)

	for _, flag := range []string{
		"workspace", "workload", "input-lens", "output-lens", "concurrencies",
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

func TestBenchWorkloadPreservesExplicitRequestRate(t *testing.T) {
	cmd := NewBenchCmd()
	cmd.SetArgs([]string{"chat", "--workload", "throughput", "--request-rate", "0"})
	cmd.SetOut(&bytes.Buffer{})

	err := cmd.Execute()
	require.ErrorContains(t, err, "request-rate")
}

func TestBenchWorkloadRejectsUnknownPreset(t *testing.T) {
	cmd := NewBenchCmd()
	cmd.SetArgs([]string{"chat", "--workload", "unknown"})
	cmd.SetOut(&bytes.Buffer{})

	err := cmd.Execute()
	require.ErrorContains(t, err, "workload must be")
}

func TestBenchmarkContextCancelsOnSignal(t *testing.T) {
	if os.Getenv("NEUTREE_BENCH_SIGNAL_TEST") == "1" {
		ctx, stop := benchmarkContext(context.Background())
		defer stop()

		_, _ = fmt.Fprintln(os.Stdout, "ready")
		<-ctx.Done()
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		return
	}

	for _, tc := range []struct {
		name   string
		signal os.Signal
	}{
		{name: "SIGINT", signal: os.Interrupt},
		{name: "SIGTERM", signal: syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBenchmarkContextCancelsOnSignal$")
			child.Env = append(os.Environ(), "NEUTREE_BENCH_SIGNAL_TEST=1")
			stdout, err := child.StdoutPipe()
			require.NoError(t, err)
			require.NoError(t, child.Start())
			t.Cleanup(func() {
				cancel()
				_ = child.Wait()
			})

			line, err := bufio.NewReader(stdout).ReadString('\n')
			require.NoError(t, err)
			require.Equal(t, "ready\n", line)
			require.NoError(t, child.Process.Signal(tc.signal))
			require.NoError(t, child.Wait())
		})
	}
}

func TestBenchmarkContextInheritsCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, stop := benchmarkContext(parent)
	defer stop()

	cancel()
	<-ctx.Done()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}
