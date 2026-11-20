//go:build unix

package bench

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestSSHProcessIsolatedFromTerminalInterrupt(t *testing.T) {
	const childEnvironment = "NEUTREE_BENCH_SSH_SIGNAL_CHILD"
	if os.Getenv(childEnvironment) == "1" {
		signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		runCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		runner := &sshRemoteRunner{auth: v1.Auth{SSHUser: "bench"}, host: "test.invalid"}
		done := make(chan error, 1)
		go func() { done <- runner.Run(runCtx, "benchmark", io.Discard, io.Discard) }()
		require.Eventually(t, func() bool {
			_, err := os.Stat(os.Getenv("NEUTREE_BENCH_SSH_READY"))
			return err == nil
		}, 5*time.Second, 20*time.Millisecond)
		_, _ = fmt.Fprintln(os.Stdout, "ready")
		<-signalCtx.Done()
		select {
		case err := <-done:
			t.Fatalf("SSH stopped on the terminal signal: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("SSH did not stop after context cancellation")
		}

		return
	}

	directory := t.TempDir()
	sshProgram := filepath.Join(directory, "ssh")
	sshScript := "#!/bin/sh\nprintf ready > \"$NEUTREE_BENCH_SSH_READY\"\nexec sleep 5\n"
	require.NoError(t, os.WriteFile(sshProgram, []byte(sshScript), 0700))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSSHProcessIsolatedFromTerminalInterrupt$")
	child.Env = append(os.Environ(), childEnvironment+"=1", "NEUTREE_BENCH_SSH_READY="+filepath.Join(directory, "ready"),
		"PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	child.Stderr = os.Stderr
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		cancel()
		_ = child.Wait()
	})

	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", line)
	require.NoError(t, syscall.Kill(-child.Process.Pid, syscall.SIGINT))
	require.NoError(t, child.Wait())
}

func TestSSHRunnerBoundsWaitForInheritedPipes(t *testing.T) {
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "child.pid")
	sshScript := "#!/bin/sh\nsleep 30 &\nprintf '%s' \"$!\" > \"$NEUTREE_BENCH_SSH_CHILD_PID\"\nexec sleep 30\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "ssh"), []byte(sshScript), 0700))
	t.Setenv("NEUTREE_BENCH_SSH_CHILD_PID", pidPath)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		data, err := os.ReadFile(pidPath)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &sshRemoteRunner{auth: v1.Auth{SSHUser: "bench"}, host: "test.invalid"}
	done := make(chan error, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		done <- runner.Run(ctx, "benchmark", &stdout, &stderr)
	}()
	require.Eventually(t, func() bool {
		_, err := os.Stat(pidPath)
		return err == nil
	}, 3*time.Second, 20*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(4 * time.Second):
		t.Fatal("SSH did not return while a child held its output pipes")
	}
}
