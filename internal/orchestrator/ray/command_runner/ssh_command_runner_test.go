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
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/tmp/ray_ssh/7337224015/%C",
		"-o", "ControlPersist=10s",
		"-i", testSSHPrivateKey,
		fmt.Sprintf("%s@%s", testSSHUser, testSSHIP),
	}
)

func newSSHCommandRunner(exector *commandmocks.MockExecutor) *SSHCommandRunner {
	return NewSSHCommandRunner(testNode, testSSHIP, v1.Auth{
		SSHPrivateKey: testSSHPrivateKey,
		SSHUser:       testSSHUser,
	}, testCluster, exector.Execute)
}

func TestNewSSHCommandRunner(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	assert.Equal(t, "node1", runner.nodeID)
	assert.Equal(t, "test_cluster", runner.clusterName)
	assert.Equal(t, "test_private_key", runner.sshPrivateKey)
	assert.Equal(t, "test_user", runner.sshUser)
	assert.Equal(t, "127.0.0.1", runner.sshIP)
	assert.Equal(t, "/tmp/ray_ssh/7337224015", runner.sshControlPath)
	assert.NotEmpty(t, "/tmp/ray_ssh/7337224015")
}

func TestSSHCommandRunner_Run_Success(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	sshOptionsWithCommand := append(testSSHOptions, testCommand)
	mockExec.On("Execute", mock.Anything, "ssh", sshOptionsWithCommand).Return([]byte("success"), nil)

	output, err := runner.Run(context.Background(), testCommand, false, nil, true, nil, "", "", false)

	assert.NoError(t, err)
	assert.Equal(t, "success", output)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_WithError(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	sshOptionsWithCommand := append(testSSHOptions, testCommand)
	mockExec.On("Execute", mock.Anything, "ssh", sshOptionsWithCommand).Return([]byte("with error"), errors.New("ssh failed"))

	_, err := runner.Run(context.Background(), testCommand, false, nil, true, nil, "", "", false)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SSH command failed")
	assert.Contains(t, err.Error(), "with error")
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_ExitOnFail(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	sshOptionsWithCommand := append(testSSHOptions, testCommand)
	mockExec.On("Execute", mock.Anything, "ssh", sshOptionsWithCommand).Return([]byte("error"), errors.New("ssh failed"))

	output, err := runner.Run(context.Background(), testCommand, false, nil, true, nil, "", "", false)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "command failed")
	assert.Equal(t, "", output)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_ShutdownAfterRun(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	sshOptionsWithShutdown := append(testSSHOptions, testCommand+"; sudo shutdown -h now")
	mockExec.On("Execute", mock.Anything, "ssh", sshOptionsWithShutdown).Return([]byte("success"), nil)

	_, err := runner.Run(context.Background(), testCommand, false, nil, false, nil, "", "", true)

	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_WithPortForward(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	expectedArgs := append([]string{"ssh", "-L", "8080:localhost:8080"}, testSSHOptions...)
	expectedArgs = append(expectedArgs, testCommand)
	mockExec.On("Execute", mock.Anything, "ssh", expectedArgs[1:]).Return([]byte("success"), nil)

	_, err := runner.Run(context.Background(), testCommand, false, testPortForward, false, nil, "", "", false)
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_WithEnvironmentVariables(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "export ENV1=value1;")
		assert.Contains(t, strings.Join(cmd, " "), "export ENV2=value2;")
	}).Return([]byte("success"), nil)

	_, err := runner.Run(context.Background(), testCommand, false, nil, false, testEnvVars, "", "", false)
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestSSHCommandRunner_Run_EmptyCommand(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	expectedCmd := "while true; do sleep 86400; done"
	expectedArgs := append(testSSHOptions, expectedCmd)
	mockExec.On("Execute", mock.Anything, "ssh", expectedArgs).Return([]byte("success"), nil)

	_, err := runner.Run(context.Background(), "", false, nil, false, nil, "", "", false)
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
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/tmp/ray_ssh/7337224015/%C",
		"-o", "ControlPersist=10s",
		"-i", overrideKey,
		fmt.Sprintf("%s@%s", testSSHUser, testSSHIP),
		testCommand,
	}
	mockExec.On("Execute", mock.Anything, "ssh", expectedArgs[1:]).Return([]byte("success"), nil)

	_, err := runner.Run(context.Background(), testCommand, false, nil, false, nil, "", overrideKey, false)
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

func TestSSHCommandRunner_Run_WithOutputDisabled(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	sshOptionsWithCommand := append(testSSHOptions, testCommand)
	mockExec.On("Execute", mock.Anything, "ssh", sshOptionsWithCommand).Return([]byte("output should be ignored"), nil)

	output, err := runner.Run(context.Background(), testCommand, false, nil, false, nil, "", "", false)
	assert.NoError(t, err)
	assert.Equal(t, "", output)
	mockExec.AssertExpectations(t)
}
