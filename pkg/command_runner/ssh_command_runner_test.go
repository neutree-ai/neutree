package command_runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

var (
	testSSHPrivateKey = "test_private_key"
	testSSHUser       = "test_user"
	testSSHIP         = "127.0.0.1"
	testNode          = "node1"
	testCluster       = "test_cluster"
	testCommand       = "echo hello"
	testEnvVars       = map[string]interface{}{"ENV1": "value1", "ENV2": "value2"}
	testPortForward   = []string{"8080:localhost:8080"}
	testSSHOptions    = []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
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
	mockExec.On("Execute", mock.Anything, "ssh", sshOptionsWithCommand).Return([]byte("with error"), errors.New("ssh failed"))

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

	expectedArgs := append([]string{"ssh", "-L", "8080:localhost:8080"}, testSSHOptions...)
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
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
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
	assert.Contains(t, err.Error(), "ssh connection to node",
		"message should identify the precheck phase")
	assert.Contains(t, err.Error(), "failed:",
		"message should read naturally without embedding the sentinel text as prose")
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

func TestSSHCommandRunner_CheckConnection_PreservesExecutorOutputAsConnectionFailureDetail(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "uptime")
	}).Return([]byte("ssh: connect to host 127.0.0.1 port 22: Connection refused\n"), errors.New("exit status 255")).Once()

	_, err := runner.Run(context.Background(), testCommand, false, nil, true, nil, "", false)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrConnectionFailed),
		"errors.Is must report ErrConnectionFailed; got %v", err)
	assert.Contains(t, err.Error(), "exit status 255",
		"message should preserve the process exit status")
	assert.Contains(t, err.Error(), "Connection refused",
		"message should preserve SSH stderr returned as executor output")
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
