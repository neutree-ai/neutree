package command_runner

import (
	"context"
	"strings"
	"testing"

	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestSSHFileClient_WriteFileIfChanged_SkipsWhenContentMatches(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)
	content := []byte("scrape_configs: []\n")

	mockExec.On("Execute", mock.Anything, "ssh", sshArgsContaining("uptime")).
		Return([]byte("success"), nil).
		Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, "sha256sum") &&
			strings.Contains(joined, "/etc/vmagent/config.yaml")
	})).
		Return([]byte(sha256Hex(content)+"\n"), nil).
		Once()

	changed, err := runner.Files().WriteFileIfChanged(
		context.Background(),
		"/etc/vmagent/config.yaml",
		content,
		WriteFileOptions{Sudo: true},
	)

	require.NoError(t, err)
	assert.False(t, changed)
	mockExec.AssertNotCalled(t, "Execute", mock.Anything, "scp", mock.Anything)
	mockExec.AssertExpectations(t)
}

func TestSSHFileClient_WriteFile_UploadsAndInstallsAtomically(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)
	content := []byte("metrics config")

	mockExec.On("Execute", mock.Anything, "scp", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, "-i "+testSSHPrivateKey) &&
			strings.Contains(joined, testSSHUser+"@"+testSSHIP+":/tmp/neutree-remote-file-")
	})).Return([]byte(""), nil).Once()

	mockExec.On("Execute", mock.Anything, "ssh", sshArgsContaining("uptime")).
		Return([]byte("success"), nil).
		Twice()
	mockExec.On("Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, "mkdir -p") &&
			strings.Contains(joined, "/etc/neutree")
	})).
		Return([]byte(""), nil).
		Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, "install -m 0640") &&
			strings.Contains(joined, "root") &&
			strings.Contains(joined, "neutree-tmp") &&
			strings.Contains(joined, "mv") &&
			strings.Contains(joined, "/etc/neutree/metrics.yaml")
	})).Return([]byte(""), nil).Once()

	err := runner.Files().WriteFile(
		context.Background(),
		"/etc/neutree/metrics.yaml",
		content,
		WriteFileOptions{
			Mode:         "0640",
			Owner:        "root",
			Group:        "root",
			Sudo:         true,
			Atomic:       true,
			CreateParent: true,
		},
	)

	require.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func TestSSHFileClient_WriteFileIfChanged_WritesWhenContentDiffers(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)
	content := []byte("scrape_configs:\n- job_name: neutree-metrics\n")

	mockExec.On("Execute", mock.Anything, "ssh", sshArgsContaining("uptime")).
		Return([]byte("success"), nil).
		Twice()
	mockExec.On("Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, "sha256sum") &&
			strings.Contains(joined, "/etc/vmagent/config.yaml")
	})).
		Return([]byte("different-hash\n"), nil).
		Once()
	mockExec.On("Execute", mock.Anything, "scp", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, testSSHUser+"@"+testSSHIP+":/tmp/neutree-remote-file-")
	})).Return([]byte(""), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, "install -m 0644") &&
			strings.Contains(joined, "/etc/vmagent/config.yaml") &&
			!strings.Contains(joined, "neutree-tmp") &&
			!strings.Contains(joined, " mv ")
	})).Return([]byte(""), nil).Once()

	changed, err := runner.Files().WriteFileIfChanged(
		context.Background(),
		"/etc/vmagent/config.yaml",
		content,
		WriteFileOptions{},
	)

	require.NoError(t, err)
	assert.True(t, changed)
	mockExec.AssertExpectations(t)
}

func TestSSHFileClient_ReadStatAndRemove(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	mockExec.On("Execute", mock.Anything, "ssh", sshArgsContaining("uptime")).
		Return([]byte("success"), nil).
		Times(3)
	mockExec.On("Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, "sudo sh -c") &&
			strings.Contains(joined, "cat") &&
			strings.Contains(joined, "/etc/neutree/metrics.yaml")
	})).
		Return([]byte("body"), nil).
		Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, "stat -c") &&
			strings.Contains(joined, "/etc/neutree/metrics.yaml")
	})).
		Return([]byte("12 640 root neutree\n"), nil).
		Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, "rm -f") &&
			strings.Contains(joined, "/etc/neutree/metrics.yaml")
	})).
		Return([]byte(""), nil).
		Once()

	body, err := runner.Files().ReadFile(
		context.Background(),
		"/etc/neutree/metrics.yaml",
		ReadFileOptions{Sudo: true},
	)
	require.NoError(t, err)
	assert.Equal(t, []byte("body"), body)

	stat, err := runner.Files().Stat(
		context.Background(),
		"/etc/neutree/metrics.yaml",
		StatFileOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, "/etc/neutree/metrics.yaml", stat.Path)
	assert.Equal(t, int64(12), stat.Size)
	assert.Equal(t, "640", stat.Mode)
	assert.Equal(t, "root", stat.Owner)
	assert.Equal(t, "neutree", stat.Group)

	err = runner.Files().Remove(
		context.Background(),
		"/etc/neutree/metrics.yaml",
		RemoveFileOptions{Sudo: true},
	)
	require.NoError(t, err)
	mockExec.AssertExpectations(t)
}

func sshArgsContaining(want string) interface{} {
	return mock.MatchedBy(func(args []string) bool {
		return strings.Contains(strings.Join(args, " "), want)
	})
}
