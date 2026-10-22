package command

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

func TestOSExecutorExecuteOutput(t *testing.T) {
	cases := []struct {
		name   string
		script string
		stdout string
	}{
		{name: "stdout only", script: "printf stdout", stdout: "stdout"},
		{name: "separate streams without newline", script: "printf stdout; printf stderr >&2", stdout: "stdout"},
		{name: "stderr only success", script: "printf diagnostic >&2"},
		{
			name:   "earlier command fails but final command succeeds",
			script: "neutree_missing_startup_command; printf 'd803\\n'",
			stdout: "d803\n",
		},
		{
			name:   "successful JSON with startup stderr noise",
			script: "printf startup-warning >&2; printf '{\"status\":\"ready\"}'",
			stdout: `{"status":"ready"}`,
		},
		{
			name:   "startup stdout noise retained",
			script: "printf 'welcome\\n{\"status\":\"ready\"}'; printf warning >&2",
			stdout: "welcome\n{\"status\":\"ready\"}",
		},
		{
			name:   "stdout bytes remain unchanged",
			script: "printf ' \\000\\377\\r\\nwelcome\\n\\n'; printf warning >&2",
			stdout: " \x00\xff\r\nwelcome\n\n",
		},
		{
			name:   "stdout is not capped by stderr limit",
			script: "printf '%020000d' 0; printf warning >&2",
			stdout: strings.Repeat("0", 20000),
		},
		{name: "empty output", script: ":"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var executor Executor = &OSExecutor{}
			output, err := executor.Execute(ctx, "sh", []string{"-c", testCase.script})
			require.NoError(t, err)
			assert.Equal(t, testCase.stdout, string(output))
		})
	}
}

func TestOSExecutorExecuteExitError(t *testing.T) {
	cases := []struct {
		name   string
		script string
		stdout string
		stderr string
	}{
		{name: "empty stderr", script: "printf partial; exit 23", stdout: "partial"},
		{name: "stderr diagnostic", script: "printf partial; printf failure >&2; exit 23", stdout: "partial", stderr: "failure"},
		{name: "stderr only", script: "printf failure >&2; exit 23", stderr: "failure"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			executor := &OSExecutor{}
			output, err := executor.Execute(context.Background(), "sh", []string{"-c", testCase.script})
			require.Error(t, err)
			var exitError *exec.ExitError
			require.ErrorAs(t, err, &exitError)
			assert.Equal(t, 23, exitError.ExitCode())
			assert.Equal(t, testCase.stdout, string(output))
			if testCase.stderr == "" {
				assert.Equal(t, exitError.Error(), err.Error())
			} else {
				assert.Contains(t, err.Error(), testCase.stderr)
			}
		})
	}
}

func TestOSExecutorExecuteStartError(t *testing.T) {
	cases := []struct {
		name    string
		command string
		cause   error
	}{
		{
			name:    "not found in PATH",
			command: "neutree-os-executor-command-that-does-not-exist",
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
			executor := &OSExecutor{}
			output, err := executor.Execute(context.Background(), testCase.command, nil)
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

func TestOSExecutorExecuteContextDoneBeforeStart(t *testing.T) {
	cases := []struct {
		name     string
		deadline bool
		cause    error
	}{
		{name: "already canceled", cause: context.Canceled},
		{name: "already expired", deadline: true, cause: context.DeadlineExceeded},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if testCase.deadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else {
				cancel()
			}
			defer cancel()
			executor := &OSExecutor{}
			output, err := executor.Execute(ctx, "sh", []string{"-c", "printf unexpected"})
			require.ErrorIs(t, err, testCase.cause)
			assert.Empty(t, output)
		})
	}
}

func TestOSExecutorExecuteCancellation(t *testing.T) {
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
			executor := &OSExecutor{}
			started := time.Now()
			output, err := executor.Execute(ctx, "sh", []string{"-c", "printf started; printf diagnostic >&2; exec sleep 10"})
			require.ErrorIs(t, err, testCase.cause)
			var exitError *exec.ExitError
			assert.ErrorAs(t, err, &exitError)
			assert.Equal(t, "started", string(output))
			assert.Contains(t, err.Error(), "diagnostic")
			assert.Less(t, time.Since(started), 3*time.Second)
		})
	}
}

func TestOSExecutorExecuteWithTimeout(t *testing.T) {
	cases := []struct {
		name          string
		script        string
		timeout       time.Duration
		parentTimeout time.Duration
		parentCancel  bool
		stdout        string
		stderr        string
		cause         error
		exitCode      int
	}{
		{
			name:    "success uses stdout only",
			script:  "printf warning >&2; printf ' {\"ready\":true}\\n'",
			timeout: 5 * time.Second,
			stdout:  " {\"ready\":true}\n",
		},
		{
			name:     "failure preserves diagnostics and exit error",
			script:   "printf partial; printf failure >&2; exit 23",
			timeout:  5 * time.Second,
			stdout:   "partial",
			stderr:   "failure",
			exitCode: 23,
		},
		{
			name:    "timeout kills running process",
			script:  "printf started; printf diagnostic >&2; exec sleep 10",
			timeout: 500 * time.Millisecond,
			stdout:  "started",
			stderr:  "diagnostic",
			cause:   context.DeadlineExceeded,
		},
		{
			name:          "earlier parent deadline wins",
			script:        "printf started; printf diagnostic >&2; exec sleep 10",
			timeout:       5 * time.Second,
			parentTimeout: 500 * time.Millisecond,
			stdout:        "started",
			stderr:        "diagnostic",
			cause:         context.DeadlineExceeded,
		},
		{
			name:         "already canceled parent",
			script:       "printf unexpected",
			timeout:      5 * time.Second,
			parentCancel: true,
			cause:        context.Canceled,
		},
		{
			name:    "already expired timeout",
			script:  "printf unexpected",
			timeout: -time.Second,
			cause:   context.DeadlineExceeded,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			if testCase.parentTimeout > 0 {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), testCase.parentTimeout)
			}
			defer cancel()
			if testCase.parentCancel {
				cancel()
			}
			executor := &OSExecutor{}
			started := time.Now()
			output, err := executor.ExecuteWithTimeout(ctx, testCase.timeout, "sh", []string{"-c", testCase.script})
			if testCase.cause != nil {
				require.ErrorIs(t, err, testCase.cause)
			} else if testCase.exitCode > 0 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, testCase.stdout, string(output))
			if testCase.stderr != "" {
				assert.Contains(t, err.Error(), testCase.stderr)
				var exitError *exec.ExitError
				require.ErrorAs(t, err, &exitError)
				if testCase.exitCode > 0 {
					assert.Equal(t, testCase.exitCode, exitError.ExitCode())
				}
			}
			assert.Less(t, time.Since(started), 3*time.Second)
		})
	}
}

func TestOSExecutorExecuteStderrLimit(t *testing.T) {
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
		for _, exitCode := range []int{0, 23} {
			t.Run(testCase.name+"/exit_"+strconv.Itoa(exitCode), func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				executor := &OSExecutor{}
				output, err := executor.Execute(ctx, "sh", []string{
					"-c", "printf '%0*d' \"$1\" 0 >&2; printf stdout-after-stderr; exit \"$2\"",
					"executor-test", strconv.Itoa(testCase.size), strconv.Itoa(exitCode),
				})
				assert.Equal(t, "stdout-after-stderr", string(output))
				if exitCode == 0 {
					require.NoError(t, err)
					return
				}
				require.Error(t, err)
				var exitError *exec.ExitError
				require.ErrorAs(t, err, &exitError)
				assert.Equal(t, exitCode, exitError.ExitCode())
				expected := strings.Repeat("0", testCase.size)
				if testCase.size > limit {
					expected = "[stderr truncated]\n" + strings.Repeat("0", limit-len("[stderr truncated]\n"))
				} else {
					assert.NotContains(t, err.Error(), "[stderr truncated]\n")
				}
				assert.Contains(t, err.Error(), expected)
				assert.LessOrEqual(t, len(err.Error()), limit+256)
			})
		}
	}
}

func TestStderrBufferTailRetention(t *testing.T) {
	const (
		limit  = 16 * 1024
		marker = "[stderr truncated]\n"
	)
	require.Equal(t, limit, stderrLimit)
	require.Equal(t, marker, stderrTruncated)
	cases := []struct {
		name   string
		writes []string
	}{
		{name: "no writes"},
		{name: "empty write", writes: []string{""}},
		{name: "below limit", writes: []string{strings.Repeat("a", limit-1)}},
		{name: "exact limit", writes: []string{strings.Repeat("a", limit)}},
		{name: "one byte over limit", writes: []string{strings.Repeat("a", limit+1)}},
		{name: "single oversized write", writes: []string{strings.Repeat("startup noise", limit) + "No such object"}},
		{
			name:   "oversized write replaces buffered bytes",
			writes: []string{"old bytes", strings.Repeat("new bytes", limit), "final diagnostic"},
		},
		{
			name:   "exact limit replaces buffered bytes",
			writes: []string{"old bytes", strings.Repeat("b", limit)},
		},
		{
			name:   "segmented sliding window",
			writes: []string{strings.Repeat("a", limit/2), strings.Repeat("b", limit/2), "c", strings.Repeat("d", limit/2), "end"},
		},
		{name: "empty write after truncation", writes: []string{strings.Repeat("a", limit+1), ""}},
		{name: "binary tail", writes: []string{strings.Repeat("a", limit), "\x00\xff\r\n"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var buffer stderrBuffer
			var complete string
			require.Empty(t, buffer.String())
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
				require.Equal(t, expected, buffer.String())
				assert.LessOrEqual(t, len(buffer.String()), limit)
			}
		})
	}
}

func TestOSExecutorExecuteRetainsFinalFailureDiagnostic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	executor := &OSExecutor{}
	output, err := executor.Execute(ctx, "sh", []string{
		"-c", "printf '%020000d' 0 >&2; printf '\\nError: No such object: missing-container' >&2; printf partial; exit 1",
	})
	require.Error(t, err)
	var exitError *exec.ExitError
	require.ErrorAs(t, err, &exitError)
	assert.Equal(t, 1, exitError.ExitCode())
	assert.Equal(t, "partial", string(output))
	assert.Contains(t, err.Error(), "[stderr truncated]\n")
	assert.Contains(t, err.Error(), "\nError: No such object: missing-container")
	assert.LessOrEqual(t, len(err.Error()), 16*1024+256)
}

func TestOSExecutorExecuteInheritedPipes(t *testing.T) {
	require.Equal(t, time.Second, processWaitDelay)
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
			executor := &OSExecutor{}
			started := time.Now()
			output, err := executor.Execute(ctx, "sh", []string{"-c", script})
			require.ErrorIs(t, err, testCase.cause)
			assert.Equal(t, "started", string(output))
			assert.Contains(t, err.Error(), "diagnostic")
			assert.Less(t, time.Since(started), 2500*time.Millisecond)
			if testCase.cancel {
				var exitError *exec.ExitError
				assert.ErrorAs(t, err, &exitError)
			}
		})
	}
}
