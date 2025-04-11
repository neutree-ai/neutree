package ray

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
)

func TestNewRayClusterManager(t *testing.T) {
	tests := []struct {
		name        string
		config      *v1.RayClusterConfig
		expectError bool
	}{
		{
			name:        "nil config",
			config:      nil,
			expectError: true,
		},
		{
			name: "valid config",
			config: &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Auth: v1.Auth{
					SSHUser:       "user",
					SSHPrivateKey: "dGVzdAo=",
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setUp()
			defer tearDown()

			mockExec := new(commandmocks.MockExecutor)
			manager, err := NewRayClusterManager(tt.config, mockExec)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, manager)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, manager)
				assert.Equal(t, tt.config.ClusterName, manager.config.ClusterName)
			}
		})
	}
}

func TestDownCluster_Success(t *testing.T) {
	setUp()
	defer tearDown()

	mockExec := new(commandmocks.MockExecutor)
	mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "down -y -v")
	}).Return([]byte("success"), nil)

	config := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
	}

	manager, err := NewRayClusterManager(config, mockExec)
	require.NoError(t, err)

	err = manager.DownCluster(context.Background())
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestDownCluster_Failure(t *testing.T) {
	setUp()
	defer tearDown()

	mockExec := new(commandmocks.MockExecutor)
	mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Return([]byte("error"), errors.New("failed"))

	config := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
	}

	setUp()
	defer tearDown()

	manager, err := NewRayClusterManager(config, mockExec)
	require.NoError(t, err)

	err = manager.DownCluster(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to down cluster")
	mockExec.AssertExpectations(t)
}

func TestUpCluster_Success(t *testing.T) {
	tests := []struct {
		name    string
		restart bool
		headIP  string
	}{
		{
			name:    "with restart",
			restart: true,
			headIP:  "1.1.1.1",
		},
		{
			name:    "without restart",
			restart: false,
			headIP:  "2.2.2.2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setUp()
			defer tearDown()

			mockExec := new(commandmocks.MockExecutor)
			mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Run(func(args mock.Arguments) {
				cmd := args.Get(2).([]string)
				assert.Contains(t, strings.Join(cmd, " "), "up")
			}).Return([]byte("success"), nil).Once()
			mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Return([]byte(tt.headIP), nil).Once()

			config := &v1.RayClusterConfig{
				ClusterName: "test-cluster",
				Auth: v1.Auth{
					SSHUser:       "user",
					SSHPrivateKey: "dGVzdAo=",
				},
			}

			manager, err := NewRayClusterManager(config, mockExec)
			require.NoError(t, err)

			ip, err := manager.UpCluster(context.Background(), tt.restart)
			assert.NoError(t, err)
			assert.Equal(t, tt.headIP, ip)
			mockExec.AssertExpectations(t)
		})
	}
}

func TestUpCluster_Failure(t *testing.T) {
	setUp()
	defer tearDown()

	mockExec := new(commandmocks.MockExecutor)
	mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Return([]byte("error"), errors.New("failed"))

	config := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
	}

	manager, err := NewRayClusterManager(config, mockExec)
	require.NoError(t, err)

	_, err = manager.UpCluster(context.Background(), true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to up cluster")
	mockExec.AssertExpectations(t)
}

func TestGetHeadIP_Success(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Return([]byte("1.1.1.1\n"), nil)

	config := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
	}

	setUp()
	defer tearDown()

	manager, err := NewRayClusterManager(config, mockExec)
	require.NoError(t, err)

	ip, err := manager.GetHeadIP(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "1.1.1.1", ip)
	mockExec.AssertExpectations(t)
}

func TestGetHeadIP_EmptyOutput(t *testing.T) {
	setUp()
	defer tearDown()

	mockExec := new(commandmocks.MockExecutor)
	mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "get-head-ip")
	}).Return([]byte(""), nil)

	config := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
	}

	manager, err := NewRayClusterManager(config, mockExec)
	require.NoError(t, err)

	_, err = manager.GetHeadIP(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty output")
	mockExec.AssertExpectations(t)
}

func TestDrainNode_Success(t *testing.T) {
	setUp()
	defer tearDown()

	mockExec := new(commandmocks.MockExecutor)
	mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "get-head-ip")
	}).Return([]byte("1.1.1.1"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Return([]byte("success"), nil).Once()

	config := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
	}

	manager, err := NewRayClusterManager(config, mockExec)
	require.NoError(t, err)

	err = manager.DrainNode(context.Background(), "node1", "reason", "message", 60)
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestStartNode_Success(t *testing.T) {
	setUp()
	defer tearDown()

	mockExec := new(commandmocks.MockExecutor)
	// get head ip
	mockExec.On("Execute", mock.Anything, "ray", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "get-head-ip")
	}).Return([]byte("1.1.1.1"), nil).Once()
	// init command
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "cmd1")
	}).Return([]byte("success"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "cmd2")
	}).Return([]byte("success"), nil).Once()
	// check docker installed
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("docker"), nil).Once()
	// check image
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte(""), nil).Once()
	// check container status
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("true"), nil).Once()
	// ray start
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "ray start")
	}).Return([]byte(""), nil).Once()

	config := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
		Docker: v1.Docker{
			ContainerName: "test-container",
		},
		InitializationCommands:       []string{"cmd1", "cmd2"},
		StaticWorkerStartRayCommands: []string{"ray start"},
	}

	manager, err := NewRayClusterManager(config, mockExec)
	require.NoError(t, err)

	err = manager.StartNode(context.Background(), "2.2.2.2")
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestStopNode_Success(t *testing.T) {
	setUp()
	defer tearDown()

	mockExec := new(commandmocks.MockExecutor)
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("docker"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("true"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "ray stop")
	}).Return([]byte(""), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Run(func(args mock.Arguments) {
		cmd := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmd, " "), "docker stop")
	}).Return([]byte(""), nil).Once()

	config := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
		Docker: v1.Docker{
			ContainerName: "test-container",
		},
	}

	manager, err := NewRayClusterManager(config, mockExec)
	require.NoError(t, err)

	err = manager.StopNode(context.Background(), "2.2.2.2")
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestStopNode_DockerNotInstalled(t *testing.T) {
	setUp()
	defer tearDown()

	mockExec := new(commandmocks.MockExecutor)
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("false"), nil).Once()

	config := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
		Docker: v1.Docker{
			ContainerName: "test-container",
		},
	}

	manager, err := NewRayClusterManager(config, mockExec)
	require.NoError(t, err)

	err = manager.StopNode(context.Background(), "2.2.2.2")
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestStopNode_ContainerNotRunning(t *testing.T) {
	setUp()
	defer tearDown()

	mockExec := new(commandmocks.MockExecutor)
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("docker"), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.Anything).Return([]byte("false"), nil).Once()

	config := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
		Docker: v1.Docker{
			ContainerName: "test-container",
		},
	}

	manager, err := NewRayClusterManager(config, mockExec)
	require.NoError(t, err)

	err = manager.StopNode(context.Background(), "2.2.2.2")
	assert.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestBuildSSHCommandArgs(t *testing.T) {
	config := &v1.RayClusterConfig{
		ClusterName: "test-cluster",
		Auth: v1.Auth{
			SSHUser:       "user",
			SSHPrivateKey: "dGVzdAo=",
		},
	}

	mockExec := new(commandmocks.MockExecutor)
	manager, err := NewRayClusterManager(config, mockExec)
	require.NoError(t, err)

	args := manager.buildSSHCommandArgs("1.1.1.1")
	assert.Equal(t, "1.1.1.1", args.NodeID)
	assert.Equal(t, "1.1.1.1", args.SshIP)
	assert.Equal(t, "user", args.AuthConfig.SSHUser)
	assert.Equal(t, "test-cluster", args.ClusterName)
}

func setUp() {
	os.Setenv("TMPDIR", "tmp")
}

func tearDown() {
	os.Unsetenv("TMPDIR")
}
