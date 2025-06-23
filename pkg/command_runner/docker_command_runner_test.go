package command_runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
)

func newDockerCommandRunner(dockerConfig *v1.Docker, executor *commandmocks.MockExecutor) *DockerCommandRunner {
	authConfig := v1.Auth{
		SSHPrivateKey: testSSHPrivateKey,
		SSHUser:       testSSHUser,
	}

	sshConfig := &CommonArgs{
		NodeID:         testNode,
		AuthConfig:     authConfig,
		SshIP:          testSSHIP,
		ProcessExecute: executor.Execute,
	}

	return NewDockerCommandRunner(dockerConfig, sshConfig)
}

func TestNewDockerCommandRunner(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	dockerConfig := &v1.Docker{
		ContainerName: "test-container",
	}

	runner := newDockerCommandRunner(dockerConfig, mockExec)
	assert.NotNil(t, runner)
	assert.Equal(t, "docker", runner.dockerCmd)
	assert.Equal(t, dockerConfig, runner.dockerConfig)
}

func TestDockerCommandRunner_Run(t *testing.T) {
	tests := []struct {
		name        string
		runEnv      string
		cmd         string
		expectCmd   string
		setupMock   func(*commandmocks.MockExecutor)
		expectError bool
	}{
		{
			name:   "host environment",
			runEnv: "host",
			cmd:    "echo hello",
			setupMock: func(m *commandmocks.MockExecutor) {
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("success"), nil)
			},
		},
		{
			name:   "docker environment",
			runEnv: "docker",
			cmd:    "echo hello",
			setupMock: func(m *commandmocks.MockExecutor) {
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("success"), nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(commandmocks.MockExecutor)
			tt.setupMock(mockExecutor)

			runner := newDockerCommandRunner(&v1.Docker{
				ContainerName: "test-container",
			}, mockExecutor)

			_, err := runner.Run(context.Background(), tt.cmd, false, nil, true, nil, tt.runEnv, "", false)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockExecutor.AssertExpectations(t)
		})
	}
}

func TestCheckContainerStatus_ContainerRunning(t *testing.T) {
	mockExecutor := new(commandmocks.MockExecutor)
	mockExecutor.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("true"), nil)

	runner := newDockerCommandRunner(&v1.Docker{
		ContainerName: "test-container",
	}, mockExecutor)

	running, err := runner.CheckContainerStatus(context.Background())
	assert.NoError(t, err)
	assert.True(t, running)
}

func TestCheckContainerStatus_ContainerNotRunning(t *testing.T) {
	mockExecutor := new(commandmocks.MockExecutor)
	mockExecutor.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("false"), nil)

	runner := newDockerCommandRunner(&v1.Docker{
		ContainerName: "test-container",
	}, mockExecutor)

	running, err := runner.CheckContainerStatus(context.Background())
	assert.NoError(t, err)
	assert.False(t, running)
}

func TestCheckContainerStatus_ContainerNotFound(t *testing.T) {
	mockExecutor := new(commandmocks.MockExecutor)
	mockExecutor.On("Execute", mock.Anything, "ssh", mock.Anything).
		Return([]byte("No such object"), errors.New(""))

	runner := newDockerCommandRunner(&v1.Docker{
		ContainerName: "test-container",
	}, mockExecutor)

	running, err := runner.CheckContainerStatus(context.Background())
	assert.NoError(t, err)
	assert.False(t, running)
}

func TestCheckContainerStatus_Error(t *testing.T) {
	mockExecutor := new(commandmocks.MockExecutor)
	mockExecutor.On("Execute", mock.Anything, "ssh", mock.Anything).
		Return([]byte(""), errors.New("other error"))

	runner := newDockerCommandRunner(&v1.Docker{
		ContainerName: "test-container",
	}, mockExecutor)

	_, err := runner.CheckContainerStatus(context.Background())
	assert.Error(t, err)
}

func TestDockerExpandUser(t *testing.T) {
	mockExecutor := new(commandmocks.MockExecutor)
	mockExecutor.On("Execute", mock.Anything, "ssh", mock.Anything).
		Return([]byte("/home/user"), nil)

	runner := newDockerCommandRunner(&v1.Docker{
		ContainerName: "test-container",
	}, mockExecutor)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no tilde", "ls /tmp", "ls /tmp"},
		{"tilde at start", "~/file", "/home/user/file"},
		{"tilde in middle", "cd ~/dir", "cd /home/user/dir"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runner.dockerExpandUser(context.Background(), tt.input, true)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRunInit(t *testing.T) {
	tests := []struct {
		name          string
		pullBeforeRun bool
		imageExists   bool
		setupMock     func(*commandmocks.MockExecutor)
		expectError   bool
	}{
		{
			name:          "pull before run",
			pullBeforeRun: true,
			setupMock: func(m *commandmocks.MockExecutor) {
				// check docker install
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
					sshCommand := args.Get(2).([]string)
					assert.Contains(t, strings.Join(sshCommand, " "), "docker")
				}).Return([]byte("docker"), nil).Once()
				// pull image
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
					sshCommand := args.Get(2).([]string)
					assert.Contains(t, strings.Join(sshCommand, " "), "docker pull")
				}).Return([]byte(""), nil).Once()
				// check container status
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
					sshCommand := args.Get(2).([]string)
					assert.Contains(t, strings.Join(sshCommand, " "), "docker inspect -f")
				}).Return([]byte("true"), nil).Once()
			},
		},
		{
			name:          "image exists",
			pullBeforeRun: false,
			imageExists:   true,
			setupMock: func(m *commandmocks.MockExecutor) {
				// check docker install
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
					sshCommand := args.Get(2).([]string)
					assert.Contains(t, strings.Join(sshCommand, " "), "docker")
				}).Return([]byte("docker"), nil).Once()
				// pull image
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
					sshCommand := args.Get(2).([]string)
					assert.Contains(t, strings.Join(sshCommand, " "), "docker image inspect")
				}).Return([]byte(""), nil).Once()
				// check container status
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
					sshCommand := args.Get(2).([]string)
					assert.Contains(t, strings.Join(sshCommand, " "), "docker inspect -f")
				}).Return([]byte("true"), nil).Once()
			},
		},
		{
			name:          "start container",
			pullBeforeRun: false,
			imageExists:   true,
			setupMock: func(m *commandmocks.MockExecutor) {
				// check docker install
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
					sshCommand := args.Get(2).([]string)
					assert.Contains(t, strings.Join(sshCommand, " "), "docker")
				}).Return([]byte("docker"), nil).Once()
				// pull image
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
					sshCommand := args.Get(2).([]string)
					assert.Contains(t, strings.Join(sshCommand, " "), "docker image inspect")
				}).Return([]byte(""), nil).Once()
				// check container status
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
					sshCommand := args.Get(2).([]string)
					assert.Contains(t, strings.Join(sshCommand, " "), "docker inspect -f")
				}).Return([]byte("false"), nil).Once()
				// run container
				m.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
					sshCommand := args.Get(2).([]string)
					assert.Contains(t, strings.Join(sshCommand, " "), "docker run")
				}).Return([]byte(""), nil).Once()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(commandmocks.MockExecutor)
			tt.setupMock(mockExecutor)

			runner := newDockerCommandRunner(&v1.Docker{
				ContainerName: "test-container",
				Image:         "test-image",
				PullBeforeRun: tt.pullBeforeRun,
				RunOptions:    []string{"--shm-size=1g"},
			}, mockExecutor)

			_, err := runner.RunInit(context.Background())
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockExecutor.AssertExpectations(t)
		})
	}
}

func TestWithDockerExec(t *testing.T) {
	cmds := []string{"echo hello", "ls -l"}
	result := WithDockerExec(cmds, "test-container", "docker", nil, false)
	assert.Len(t, result, 2)
	assert.Contains(t, result[0], "docker exec")
	assert.Contains(t, result[0], "test-container")
	assert.Contains(t, result[0], "echo hello")
}

func TestPrependEnvVars(t *testing.T) {
	envVars := map[string]interface{}{
		"VAR1": "value1",
		"VAR2": 123,
	}
	cmd := "echo hello"
	result := prependEnvVars(cmd, envVars)
	assert.Contains(t, result, "export VAR1=value1;")
	assert.Contains(t, result, "export VAR2=123;")
	assert.Contains(t, result, "echo hello")
}

func TestRun_AutoEnvironment(t *testing.T) {
	tests := []struct {
		name         string
		cmd          string
		expectDocker bool
	}{
		{
			name:         "auto detects docker command",
			cmd:          "echo hello",
			expectDocker: true,
		},
		{
			name:         "auto detects host command",
			cmd:          "docker ps",
			expectDocker: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(commandmocks.MockExecutor)
			mockExecutor.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("success"), nil)

			runner := newDockerCommandRunner(&v1.Docker{
				ContainerName: "test-container",
			}, mockExecutor)

			_, err := runner.Run(context.Background(), tt.cmd, false, nil, true, nil, "auto", "", false)
			assert.NoError(t, err)

			// Verify docker exec was called if expected
			if tt.expectDocker {
				mockExecutor.AssertCalled(t, "Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
					return strings.Contains(strings.Join(args, " "), "docker exec")
				}))
			}
		})
	}
}

func TestRun_ShutdownAfterRun(t *testing.T) {
	mockExecutor := new(commandmocks.MockExecutor)
	mockExecutor.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("success"), nil)

	runner := newDockerCommandRunner(&v1.Docker{
		ContainerName: "test-container",
	}, mockExecutor)

	_, err := runner.Run(context.Background(), "echo hello", false, nil, true, nil, "host", "", true)
	assert.NoError(t, err)

	mockExecutor.AssertCalled(t, "Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
		return strings.Contains(strings.Join(args, " "), "sudo shutdown -h now")
	}))
}

func TestCheckDockerInstalled(t *testing.T) {
	tests := []struct {
		name        string
		mockOutput  string
		expect      bool
		expectError bool
		mockError   error
	}{
		{
			name:       "docker installed",
			mockOutput: "/usr/bin/docker",
			expect:     true,
		},
		{
			name:       "docker not installed",
			mockOutput: "NoExist",
			expect:     false,
		},
		{
			name:        "command error",
			mockOutput:  "",
			expect:      false,
			mockError:   errors.New("command failed"),
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(commandmocks.MockExecutor)
			mockExecutor.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte(tt.mockOutput), tt.mockError)

			runner := newDockerCommandRunner(&v1.Docker{
				ContainerName: "test-container",
			}, mockExecutor)

			installed, err := runner.CheckDockerInstalled(context.Background())
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.Equal(t, tt.expect, installed)
				assert.NoError(t, err)
			}
		})
	}
}

func TestGenerateDockerStartCommand(t *testing.T) {
	runner := &DockerCommandRunner{
		dockerCmd: "docker",
		dockerConfig: &v1.Docker{
			ContainerName: "test-container",
		},
	}

	cmd := runner.generateDockerStartCommand(
		"test-image",
		"test-container",
		"docker",
		[]string{"--gpus all"},
	)

	assert.Contains(t, cmd, "docker run")
	assert.Contains(t, cmd, "--name test-container")
	assert.Contains(t, cmd, "--net=host")
	assert.Contains(t, cmd, "test-image")
	assert.Contains(t, cmd, "--gpus all")
}

func TestAutoConfigureShm(t *testing.T) {
	tests := []struct {
		name          string
		memInfo       string
		expectShmSize bool
		runOptions    []string
	}{
		{
			name:          "with mem info",
			memInfo:       "MemAvailable: 8000000 kB",
			expectShmSize: true,
		},
		{
			name:          "no mem info",
			memInfo:       "",
			expectShmSize: false,
		},
		{
			name:          "with shm size in options",
			memInfo:       "MemAvailable: 8000000 kB",
			expectShmSize: true,
			runOptions:    []string{"--shm-size=10000000"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(commandmocks.MockExecutor)
			mockExecutor.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte(tt.memInfo), nil)

			runner := newDockerCommandRunner(&v1.Docker{
				ContainerName: "test-container",
			}, mockExecutor)

			options, err := runner.autoConfigureShm(context.Background(), tt.runOptions)
			if tt.expectShmSize {
				assert.NoError(t, err)
				assert.Contains(t, options[0], "--shm-size=")
			} else {
				assert.Error(t, err)
			}
		})
	}
}
