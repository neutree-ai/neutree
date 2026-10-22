package command_runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func installSSHOutputFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	fixture := `#!/bin/sh
for argument do
    last_argument="$argument"
done
printf '%s\n' "$*" >> "$NEU_TEST_SSH_CALLS"
printf '%s' "$NEU_TEST_SSH_STDERR" >&2
if [ "$last_argument" = uptime ]; then
    printf 'up\n'
else
    printf '%s' "$NEU_TEST_SSH_STDOUT"
    exit "${NEU_TEST_SSH_STATUS:-0}"
fi
`
	require.NoError(t, os.WriteFile(filepath.Join(directory, "ssh"), []byte(fixture), 0700))
	callsFile := filepath.Join(directory, "calls")
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NEU_TEST_SSH_CALLS", callsFile)
	t.Setenv("NEU_TEST_SSH_STDOUT", "")
	t.Setenv("NEU_TEST_SSH_STDERR", "")
	t.Setenv("NEU_TEST_SSH_STATUS", "0")
	return callsFile
}

func outputFixtureRunner(t *testing.T) *SSHCommandRunner {
	t.Helper()
	return NewSSHCommandRunner("node", "127.0.0.1", v1.Auth{SSHUser: "tester"}, "",
		func(ctx context.Context, name string, args []string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		})
}

func TestSSHCommandRunnerDefaultSeparatesStartupStderr(t *testing.T) {
	callsFile := installSSHOutputFixture(t)
	t.Setenv("NEU_TEST_SSH_STDOUT", "{\"running\":true}\n")
	t.Setenv("NEU_TEST_SSH_STDERR", "bashrc: missing-command: command not found")
	runner := outputFixtureRunner(t)
	output, err := runner.Run(context.Background(), "docker inspect fixture", true, nil, true, nil, "", false)
	require.NoError(t, err)
	assert.Equal(t, "{\"running\":true}\n", output)
	assert.True(t, json.Valid([]byte(output)))
	calls, err := os.ReadFile(callsFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	require.Len(t, lines, 2)
	assert.True(t, strings.HasPrefix(lines[0], "-T "))
	assert.True(t, strings.HasSuffix(lines[0], " uptime"))
	assert.True(t, strings.HasPrefix(lines[1], "-T "))
	assert.True(t, strings.HasSuffix(lines[1], " docker inspect fixture"))
}

func TestSSHCommandRunnerDefaultDoesNotFilterStdout(t *testing.T) {
	installSSHOutputFixture(t)
	stdout := "welcome\n{\"running\":true}\n"
	t.Setenv("NEU_TEST_SSH_STDOUT", stdout)
	output, err := outputFixtureRunner(t).Run(context.Background(), "inspect", false, nil, true, nil, "", false)
	require.NoError(t, err)
	assert.Equal(t, stdout, output)
	assert.False(t, json.Valid([]byte(output)))
}

func TestSSHCommandRunnerDefaultStderrOnlyCanSucceed(t *testing.T) {
	installSSHOutputFixture(t)
	t.Setenv("NEU_TEST_SSH_STDERR", "startup warning")
	for _, withOutput := range []bool{false, true} {
		output, err := outputFixtureRunner(t).Run(context.Background(), "no output", false, nil, withOutput, nil, "", false)
		require.NoError(t, err)
		assert.Empty(t, output)
	}
}

func TestSSHCommandRunnerDefaultPreservesFailureStderrAndCause(t *testing.T) {
	for _, exitOnFail := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "exitOnFail"}[exitOnFail], func(t *testing.T) {
			callsFile := installSSHOutputFixture(t)
			t.Setenv("NEU_TEST_SSH_STDOUT", "partial output")
			t.Setenv("NEU_TEST_SSH_STDERR", "No such object: fixture")
			t.Setenv("NEU_TEST_SSH_STATUS", "7")
			output, err := outputFixtureRunner(t).Run(context.Background(), "secret-command", exitOnFail, nil, true, nil, "", false)
			require.Error(t, err)
			assert.Empty(t, output)
			assert.Contains(t, err.Error(), "No such object: fixture")
			assert.NotContains(t, err.Error(), "partial output")
			assert.NotContains(t, err.Error(), "secret-command")
			assert.NotErrorIs(t, err, ErrConnectionFailed)
			var exitError *exec.ExitError
			require.ErrorAs(t, err, &exitError)
			assert.Equal(t, 7, exitError.ExitCode())
			calls, readErr := os.ReadFile(callsFile)
			require.NoError(t, readErr)
			assert.Len(t, strings.Split(strings.TrimSpace(string(calls)), "\n"), 2)
		})
	}
}

func TestDockerCommandRunnerDefaultSeparatesStartupStderr(t *testing.T) {
	installSSHOutputFixture(t)
	t.Setenv("NEU_TEST_SSH_STDOUT", "true\n")
	t.Setenv("NEU_TEST_SSH_STDERR", "bashrc: missing-command: command not found")
	runner := NewDockerCommandRunner(&v1.Docker{ContainerName: "fixture"}, &CommonArgs{
		NodeID: "node", SshIP: "127.0.0.1", AuthConfig: v1.Auth{SSHUser: "tester"},
	})
	running, err := runner.CheckContainerStatus(context.Background())
	require.NoError(t, err)
	assert.True(t, running)
	t.Setenv("NEU_TEST_SSH_STDOUT", "")
	t.Setenv("NEU_TEST_SSH_STDERR", strings.Repeat("startup noise\n", 2000)+"No such object: fixture")
	t.Setenv("NEU_TEST_SSH_STATUS", "1")
	running, err = runner.CheckContainerStatus(context.Background())
	require.NoError(t, err)
	assert.False(t, running)
}
