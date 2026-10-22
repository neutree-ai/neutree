package command_runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecuteSSHProcessOutput(t *testing.T) {
	cases := []struct {
		name   string
		script string
		stdout string
		stderr string
	}{
		{
			name:   "independent streams without newline",
			script: "printf stdout; printf stderr >&2",
			stdout: "stdout",
			stderr: "stderr",
		},
		{
			name:   "stderr only is successful",
			script: "printf diagnostic >&2",
			stderr: "diagnostic",
		},
		{
			name:   "stdout bytes remain unchanged",
			script: "printf ' \\000\\377\\r\\nwelcome\\n\\n'; printf warning >&2",
			stdout: " \x00\xff\r\nwelcome\n\n",
			stderr: "warning",
		},
		{
			name:   "stdout is not capped by stderr limit",
			script: "printf '%020000d' 0; printf warning >&2",
			stdout: strings.Repeat("0", 20000),
			stderr: "warning",
		},
		{
			name:   "empty output",
			script: ":",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var execute SSHProcessExecute = executeSSHProcess
			output, err := execute(ctx, "sh", []string{"-c", testCase.script})
			require.NoError(t, err)
			assert.Equal(t, testCase.stdout, output.Stdout)
			assert.Equal(t, testCase.stderr, output.Stderr)
		})
	}
}

func TestExecuteSSHProcessExitError(t *testing.T) {
	output, err := executeSSHProcess(context.Background(), "sh", []string{"-c", "printf partial; printf failure >&2; exit 23"})
	require.Error(t, err)
	var exitError *exec.ExitError
	require.ErrorAs(t, err, &exitError)
	assert.Equal(t, 23, exitError.ExitCode())
	assert.Equal(t, SSHProcessOutput{Stdout: "partial", Stderr: "failure"}, output)
}

func TestExecuteSSHProcessStartError(t *testing.T) {
	cases := []struct {
		name    string
		command string
		cause   error
	}{
		{
			name:    "not found in PATH",
			command: "neutree-ssh-process-executor-command-that-does-not-exist",
			cause:   exec.ErrNotFound,
		},
		{
			name:    "missing executable path",
			command: filepath.Join(t.TempDir(), "missing-executable"),
			cause:   os.ErrNotExist,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			output, err := executeSSHProcess(context.Background(), testCase.command, nil)
			require.ErrorIs(t, err, testCase.cause)
			assert.Empty(t, output)
			if errors.Is(err, exec.ErrNotFound) {
				var executableError *exec.Error
				assert.ErrorAs(t, err, &executableError)
			} else {
				var pathError *os.PathError
				assert.ErrorAs(t, err, &pathError)
			}
		})
	}
}

func TestExecuteSSHProcessCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output, err := executeSSHProcess(ctx, "sh", []string{"-c", "printf unexpected"})
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, output)
}

func TestExecuteSSHProcessCancellation(t *testing.T) {
	cases := []struct {
		name     string
		deadline bool
		cause    error
	}{
		{name: "cancel running process", cause: context.Canceled},
		{name: "deadline running process", deadline: true, cause: context.DeadlineExceeded},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			if testCase.deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
			}
			defer cancel()
			if !testCase.deadline {
				timer := time.AfterFunc(500*time.Millisecond, cancel)
				defer timer.Stop()
			}
			started := time.Now()
			output, err := executeSSHProcess(ctx, "sh", []string{"-c", "printf started; printf diagnostic >&2; exec sleep 10"})
			require.ErrorIs(t, err, testCase.cause)
			var exitError *exec.ExitError
			assert.ErrorAs(t, err, &exitError)
			assert.Equal(t, SSHProcessOutput{Stdout: "started", Stderr: "diagnostic"}, output)
			assert.Less(t, time.Since(started), 3*time.Second)
		})
	}
}

func TestExecuteSSHProcessStderrLimit(t *testing.T) {
	const limit = 16 * 1024
	cases := []struct {
		name string
		size int
	}{
		{name: "below limit", size: limit - 1},
		{name: "exactly at limit", size: limit},
		{name: "one byte over limit", size: limit + 1},
		{name: "large stderr is drained", size: 1024 * 1024},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			output, err := executeSSHProcess(ctx, "sh", []string{
				"-c", "printf '%0*d' \"$1\" 0 >&2; printf stdout-after-stderr", "ssh-process-test", strconv.Itoa(testCase.size),
			})
			require.NoError(t, err)
			assert.Equal(t, "stdout-after-stderr", output.Stdout)
			if testCase.size <= limit {
				assert.Equal(t, strings.Repeat("0", testCase.size), output.Stderr)
			} else {
				require.Len(t, output.Stderr, limit)
				assert.Contains(t, output.Stderr[:64], "truncated")
				assert.True(t, strings.HasSuffix(output.Stderr, strings.Repeat("0", limit-64)))
			}
		})
	}
}

func TestSSHStderrBufferTailRetention(t *testing.T) {
	const (
		limit  = 16 * 1024
		marker = "[stderr truncated]\n"
	)

	cases := []struct {
		name   string
		writes []string
	}{
		{name: "empty write", writes: []string{""}},
		{name: "below limit", writes: []string{strings.Repeat("a", limit-1)}},
		{name: "exact limit", writes: []string{strings.Repeat("a", limit)}},
		{name: "one byte over limit", writes: []string{strings.Repeat("a", limit+1)}},
		{
			name:   "single oversized write",
			writes: []string{strings.Repeat("startup noise", limit) + "No such object"},
		},
		{
			name:   "oversized write replaces buffered bytes",
			writes: []string{"old bytes", strings.Repeat("new bytes", limit), "final diagnostic"},
		},
		{
			name:   "segmented sliding window",
			writes: []string{strings.Repeat("a", limit/2), strings.Repeat("b", limit/2), "c", strings.Repeat("d", limit/2), "end"},
		},
		{
			name:   "empty write after truncation",
			writes: []string{strings.Repeat("a", limit+1), ""},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var buffer sshStderrBuffer
			var complete string

			for _, fragment := range testCase.writes {
				written, err := buffer.Write([]byte(fragment))
				require.NoError(t, err)
				require.Equal(t, len(fragment), written)
				assert.LessOrEqual(t, len(buffer.buffer), limit)
				complete += fragment
				expected := complete

				if len(complete) > limit {
					expected = marker + complete[len(complete)-limit+len(marker):]
				}

				require.Equal(t, expected, buffer.String())
			}
		})
	}
}

func TestExecuteSSHProcessRetainsFinalFailureDiagnostic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	output, err := executeSSHProcess(ctx, "sh", []string{
		"-c", "printf '%020000d' 0 >&2; printf '\\nError: No such object: missing-container' >&2; printf partial; exit 1",
	})
	require.Error(t, err)

	var exitError *exec.ExitError
	require.ErrorAs(t, err, &exitError)
	assert.Equal(t, 1, exitError.ExitCode())
	assert.Equal(t, "partial", output.Stdout)
	assert.Len(t, output.Stderr, 16*1024)
	assert.True(t, strings.HasPrefix(output.Stderr, "[stderr truncated]\n"))
	assert.True(t, strings.HasSuffix(output.Stderr, "\nError: No such object: missing-container"))
}

func TestExecuteSSHProcessInheritedPipes(t *testing.T) {
	cases := []struct {
		name   string
		cancel bool
		cause  error
	}{
		{name: "exited parent", cause: exec.ErrWaitDelay},
		{name: "canceled parent", cancel: true, cause: context.Canceled},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			script := "sleep 3 & printf started; printf diagnostic >&2"
			if testCase.cancel {
				timer := time.AfterFunc(500*time.Millisecond, cancel)
				defer timer.Stop()
				script += "; wait"
			}
			started := time.Now()
			output, err := executeSSHProcess(ctx, "sh", []string{"-c", script})
			require.ErrorIs(t, err, testCase.cause)
			assert.Equal(t, SSHProcessOutput{Stdout: "started", Stderr: "diagnostic"}, output)
			assert.Less(t, time.Since(started), 2500*time.Millisecond)
		})
	}
}
