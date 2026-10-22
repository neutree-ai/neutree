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
			name:   "large stdout remains unchanged",
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
		{
			name:   "stderr leading and trailing whitespace",
			script: "printf ' \\tfailure\\r\\n ' >&2; exit 23",
			stderr: " \tfailure\r\n ",
		},
		{name: "whitespace only stderr", script: "printf ' \\t\\r\\n ' >&2; exit 23", stderr: " \t\r\n "},
		{
			name:   "binary stderr remains unchanged",
			script: "printf ' \\000\\377\\r\\nfailure\\n\\t ' >&2; exit 23",
			stderr: " \x00\xff\r\nfailure\n\t ",
		},
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
				assert.Equal(t, exitError.Error()+": "+testCase.stderr, err.Error())
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

func TestOSExecutorExecuteFullStderr(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{name: "below 16KiB", size: 16*1024 - 1},
		{name: "exactly 16KiB", size: 16 * 1024},
		{name: "over 16KiB", size: 16*1024 + 1},
		{name: "1MiB stderr", size: 1024 * 1024},
	}

	for _, testCase := range cases {
		beginning := " \tBEGIN\x00\xff\n"
		middle := "\nMIDDLE\x00\xff\n"
		ending := "\nEND\x00\xff \r\n\t "
		padding := testCase.size - len(beginning) - len(middle) - len(ending)
		diagnostic := beginning + strings.Repeat("a", padding/2) + middle + strings.Repeat("b", padding-padding/2) + ending
		stderrPath := filepath.Join(t.TempDir(), "stderr")
		require.NoError(t, os.WriteFile(stderrPath, []byte(diagnostic), 0600))

		for _, exitCode := range []int{0, 23} {
			t.Run(testCase.name+"/exit_"+strconv.Itoa(exitCode), func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				executor := &OSExecutor{}
				output, err := executor.Execute(ctx, "sh", []string{
					"-c", "cat \"$1\" >&2; printf stdout-after-stderr; exit \"$2\"",
					"executor-test", stderrPath, strconv.Itoa(exitCode),
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
				expected := exitError.Error() + ": " + diagnostic
				assert.Equal(t, len(expected), len(err.Error()))
				assert.True(t, expected == err.Error(), "stderr diagnostic must retain every byte")
			})
		}
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
	expected := exitError.Error() + ": " + strings.Repeat("0", 20000) + "\nError: No such object: missing-container"
	assert.Equal(t, len(expected), len(err.Error()))
	assert.True(t, expected == err.Error(), "startup noise and final diagnostic must both remain intact")
}

func TestOSExecutorExecuteInheritedPipes(t *testing.T) {
	cases := []struct {
		name     string
		exitCode int
	}{
		{name: "successful parent"},
		{name: "failed parent", exitCode: 23},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			script := "(sleep 1.5; printf 'late stdout\\000\\377 \\n'; printf ' \\tlate stderr\\000\\377\\r\\n ' >&2) & " +
				"printf 'early stdout\\n'; printf ' \\tearly stderr\\n' >&2; exit \"$1\""
			executor := &OSExecutor{}
			started := time.Now()
			output, err := executor.Execute(ctx, "sh", []string{"-c", script, "executor-test", strconv.Itoa(testCase.exitCode)})
			assert.Equal(t, "early stdout\nlate stdout\x00\xff \n", string(output))
			assert.GreaterOrEqual(t, time.Since(started), 1500*time.Millisecond)
			if testCase.exitCode == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var exitError *exec.ExitError
			require.ErrorAs(t, err, &exitError)
			assert.Equal(t, testCase.exitCode, exitError.ExitCode())
			assert.Equal(t, exitError.Error()+":  \tearly stderr\n \tlate stderr\x00\xff\r\n ", err.Error())
		})
	}
}
