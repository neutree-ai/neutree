package command_runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
)

var (
	testSSHPrivateKey = "test_private_key"
	testSSHUser       = "test_user"
	testSSHIP         = "127.0.0.1"
	testNode          = "node1"
	testCommand       = "echo hello"
	testEnvVars       = map[string]interface{}{"ENV1": "value1", "ENV2": "value2"}
	testPortForward   = []string{"8080:localhost:8080"}
	testSSHOptions    = []string{
		"-T",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "IdentitiesOnly=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=3",
		"-i", testSSHPrivateKey,
		fmt.Sprintf("%s@%s", testSSHUser, testSSHIP),
	}
)

func newSSHCommandRunner(exector *commandmocks.MockExecutor) *SSHCommandRunner {
	return NewSSHCommandRunner(testNode, testSSHIP, v1.Auth{
		SSHPrivateKey: testSSHPrivateKey,
		SSHUser:       testSSHUser,
	}, "", exector.Execute)
}

func TestNewSSHCommandRunner(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	assert.Equal(t, "node1", runner.nodeID)
	assert.Equal(t, "test_private_key", runner.sshPrivateKey)
	assert.Equal(t, "test_user", runner.sshUser)
	assert.Equal(t, "127.0.0.1", runner.sshIP)
}

func TestSSHCommandRunner_Run_Success(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	sshOptionsWithCommand := append(testSSHOptions, testCommand)
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte("success"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", sshOptionsWithCommand).Return([]byte("success"), nil)

	output, err := runner.Run(context.Background(), testCommand, false, nil, true, nil, "", false)

	assert.NoError(t, err)
	assert.Equal(t, "success", output)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_WithError(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	sshOptionsWithCommand := append(testSSHOptions, testCommand)
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte("success"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", sshOptionsWithCommand).Return([]byte("partial stdout"), errors.New("ssh failed: with error"))

	_, err := runner.Run(context.Background(), testCommand, false, nil, true, nil, "", false)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SSH command failed")
	assert.Contains(t, err.Error(), "with error")
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_ExitOnFail(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	sshOptionsWithCommand := append(testSSHOptions, testCommand)
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte("success"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", sshOptionsWithCommand).Return([]byte("error"), errors.New("ssh failed"))

	output, err := runner.Run(context.Background(), testCommand, false, nil, true, nil, "", false)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "command failed")
	assert.Equal(t, "", output)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_ShutdownAfterRun(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte("success"), nil).Once()
	sshOptionsWithShutdown := append(testSSHOptions, testCommand+"; sudo shutdown -h now")
	mockExec.On("Execute", mock.Anything, "ssh", sshOptionsWithShutdown).Return([]byte("success"), nil)

	_, err := runner.Run(context.Background(), testCommand, false, nil, false, nil, "", true)

	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_WithPortForward(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	expectedArgs := append([]string{"ssh", "-T", "-L", "8080:localhost:8080"}, testSSHOptions[1:]...)
	expectedArgs = append(expectedArgs, testCommand)
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte("success"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", expectedArgs[1:]).Return([]byte("success"), nil)

	_, err := runner.Run(context.Background(), testCommand, false, testPortForward, false, nil, "", false)
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_WithEnvironmentVariables(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte("success"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "export ENV1=value1;")
		assert.Contains(t, strings.Join(cmd, " "), "export ENV2=value2;")
	}).Return([]byte("success"), nil)

	_, err := runner.Run(context.Background(), testCommand, false, nil, false, testEnvVars, "", false)
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_EmptyCommand(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	expectedCmd := "while true; do sleep 86400; done"
	expectedArgs := append(testSSHOptions, expectedCmd)
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte("success"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", expectedArgs).Return([]byte("success"), nil)

	_, err := runner.Run(context.Background(), "", false, nil, false, nil, "", false)
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_WithSSHOptionsOverride(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	overrideKey := "override_key"
	expectedArgs := []string{
		"ssh",
		"-T",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "IdentitiesOnly=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=3",
		"-i", overrideKey,
		fmt.Sprintf("%s@%s", testSSHUser, testSSHIP),
		testCommand,
	}
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte("success"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", expectedArgs[1:]).Return([]byte("success"), nil)

	_, err := runner.Run(context.Background(), testCommand, false, nil, false, nil, overrideKey, false)
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_getSSHOptions_NoControlPath(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := NewSSHCommandRunner(testNode, testSSHIP, v1.Auth{
		SSHPrivateKey: testSSHPrivateKey,
		SSHUser:       testSSHUser,
	}, "", mockExec.Execute) // Empty cluster name will result in empty control path

	options := runner.getSSHOptions("")
	assert.NotContains(t, options, "ControlMaster=auto")
	assert.NotContains(t, options, "ControlPath")
	assert.NotContains(t, options, "ControlPersist=10s")
}

func TestSSHCommandRunner_getSSHOptions_WithControlPath(t *testing.T) {
	controlPath := "/tmp/neutree-test-mux"
	runner := NewSSHCommandRunner(testNode, testSSHIP, v1.Auth{
		SSHPrivateKey: testSSHPrivateKey,
		SSHUser:       testSSHUser,
	}, controlPath, nil)

	options := runner.getSSHOptions("")
	assert.Contains(t, options, "ControlMaster=auto")
	assert.Contains(t, options, "ControlPath="+controlPath+"/%C")
	assert.Contains(t, options, "ControlPersist="+SSHControlPersist)
}

func TestSSHCommandRunner_CloseControlMaster(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	controlPath := "/tmp/neutree-test-mux"
	runner := NewSSHCommandRunner(testNode, testSSHIP, v1.Auth{
		SSHPrivateKey: testSSHPrivateKey,
		SSHUser:       testSSHUser,
	}, controlPath, mockExec.Execute)

	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "-O")
		assert.Contains(t, strings.Join(cmd, " "), "exit")
		assert.Contains(t, strings.Join(cmd, " "), "ControlPath="+controlPath+"/%C")
	}).Return([]byte(""), nil).Once()

	err := runner.CloseControlMaster(context.Background())
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_CloseControlMaster_NoControlPath(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := NewSSHCommandRunner(testNode, testSSHIP, v1.Auth{
		SSHPrivateKey: testSSHPrivateKey,
		SSHUser:       testSSHUser,
	}, "", mockExec.Execute)

	err := runner.CloseControlMaster(context.Background())
	assert.NoError(t, err)
	mockExec.AssertNotCalled(t, "Execute", mock.Anything, "ssh", mock.Anything)
}

func TestSSHCommandRunner_CheckConnection_PreservesUnderlyingErrorAsConnectionFailed(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	underlying := errors.New("exit status 255: ssh: connect to host 127.0.0.1 port 22: Connection refused")
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte(""), underlying).Once()

	_, err := runner.Run(context.Background(), testCommand, false, nil, true, nil, "", false)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrConnectionFailed),
		"errors.Is must report ErrConnectionFailed; got %v", err)
	assert.True(t, errors.Is(err, underlying),
		"errors.Is must preserve the original precheck error; got %v", err)
	assert.Contains(t, err.Error(), "ssh connection failed",
		"message should preserve the stable connection-failure substring used by e2e and operators")
	assert.Contains(t, err.Error(), "ssh connection failed to node",
		"message should identify the precheck phase")
	assert.NotContains(t, err.Error(), "node 127.0.0.1 connection failed",
		"message should not repeat the sentinel wording in the user-facing sentence")
	assert.Contains(t, err.Error(), testSSHIP,
		"message should include the target IP %q", testSSHIP)
	assert.Contains(t, err.Error(), "Connection refused",
		"message should preserve the underlying SSH stderr token")
	assert.Contains(t, err.Error(), "hint:",
		"message should include the static-cluster troubleshooting hint section")
	assert.Contains(t, err.Error(), "physical server",
		"hint should mention the physical-server IP guidance")
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_CheckConnection_PreservesExecutorErrorAsConnectionFailureDetail(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte(""), errors.New("exit status 255: ssh: connect to host 127.0.0.1 port 22: Connection refused")).Once()

	_, err := runner.Run(context.Background(), testCommand, false, nil, true, nil, "", false)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrConnectionFailed),
		"errors.Is must report ErrConnectionFailed; got %v", err)
	assert.Contains(t, err.Error(), "exit status 255",
		"message should preserve the process exit status")
	assert.Contains(t, err.Error(), "Connection refused",
		"message should preserve SSH stderr included in the executor error")
	assert.Contains(t, err.Error(), "hint:",
		"message should include the static-cluster troubleshooting hint section")
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_CheckConnection_AddsConnectTimeout(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	var precheckArgs []string
	var mainArgs []string

	// First call = precheck (uptime). Capture its argv.
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime",
			"first call must be the precheck (uptime)")
		precheckArgs = append([]string{}, cmd...)
	}).Return([]byte("ok"), nil).Once()

	// Second call = main command. Capture its argv.
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), testCommand,
			"second call must be the main command")
		mainArgs = append([]string{}, cmd...)
	}).Return([]byte("success"), nil).Once()

	_, err := runner.Run(context.Background(), testCommand, false, nil, true, nil, "", false)
	assert.NoError(t, err)

	// Precheck argv MUST include ConnectTimeout=10.
	preStr := strings.Join(precheckArgs, " ")
	assert.Contains(t, preStr, "-o ConnectTimeout=10",
		"precheck argv must carry ConnectTimeout=10; got %q", preStr)

	// And it must appear BEFORE the user@host element (SSH only honors options
	// listed before the destination).
	hostElement := fmt.Sprintf("%s@%s", testSSHUser, testSSHIP)
	hostIdx := -1
	timeoutIdx := -1
	for i, tok := range precheckArgs {
		if tok == hostElement {
			hostIdx = i
		}
		if tok == "ConnectTimeout=10" {
			timeoutIdx = i
		}
	}
	assert.GreaterOrEqual(t, hostIdx, 0, "user@host element must be present")
	assert.GreaterOrEqual(t, timeoutIdx, 0, "ConnectTimeout token must be present")
	assert.Less(t, timeoutIdx, hostIdx,
		"ConnectTimeout must precede user@host (got timeout@%d, host@%d)", timeoutIdx, hostIdx)

	// Main command argv MUST NOT carry ConnectTimeout — isolation assertion
	// (Reviewer requested keeping the option out of getSSHOptions).
	mainStr := strings.Join(mainArgs, " ")
	assert.NotContains(t, mainStr, "ConnectTimeout=10",
		"main command argv must not carry the precheck-only ConnectTimeout; got %q", mainStr)

	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_WithOutputDisabled(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	sshOptionsWithCommand := append(testSSHOptions, testCommand)
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte("success"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", sshOptionsWithCommand).Return([]byte("output should be ignored"), nil)

	output, err := runner.Run(context.Background(), testCommand, false, nil, false, nil, "", false)
	assert.NoError(t, err)
	assert.Equal(t, "", output)
	mockExec.AssertExpectations(t)
}

func installSSHOutputFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	fixture := `#!/bin/sh
for argument do
    last_argument="$argument"
done
printf '%s\n' "$*" >> "$NEU_TEST_SSH_CALLS"
if [ "$NEU_TEST_SSH_STARTUP_FAILURE" = 1 ]; then
    neutree_missing_startup_command
fi
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
	t.Setenv("NEU_TEST_SSH_STARTUP_FAILURE", "0")
	return callsFile
}

func outputFixtureRunner(t *testing.T) *SSHCommandRunner {
	t.Helper()
	return NewSSHCommandRunner("node", "127.0.0.1", v1.Auth{SSHUser: "tester"}, "", nil)
}

func TestSSHCommandRunnerDefaultSeparatesStartupStderr(t *testing.T) {
	for _, startupFailure := range []string{"0", "1"} {
		t.Run("startup_failure_"+startupFailure, func(t *testing.T) {
			callsFile := installSSHOutputFixture(t)
			t.Setenv("NEU_TEST_SSH_STARTUP_FAILURE", startupFailure)
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
		})
	}
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
			diagnostic := strings.Repeat("startup noise\n", 2000) + "No such object: fixture\n"
			t.Setenv("NEU_TEST_SSH_STDERR", diagnostic)
			t.Setenv("NEU_TEST_SSH_STATUS", "7")
			output, err := outputFixtureRunner(t).Run(context.Background(), "secret-command", exitOnFail, nil, true, nil, "", false)
			require.Error(t, err)
			assert.Empty(t, output)
			assert.True(t, strings.HasSuffix(err.Error(), diagnostic), "failure must retain the full stderr diagnostic")
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
