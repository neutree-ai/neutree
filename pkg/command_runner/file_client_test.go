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

func sshArgsContaining(want string) interface{} {
	return mock.MatchedBy(func(args []string) bool {
		return strings.Contains(strings.Join(args, " "), want)
	})
}
