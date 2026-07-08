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

func TestSSHFileClient_WriteFileIfChanged_SkipsWhenRemoteHashOutputHasWarnings(t *testing.T) {
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
		Return([]byte(strings.Join([]string{
			"sudo: unable to resolve host x86-2204: Name or service not known",
			strings.ToUpper(sha256Hex(content)) + "  /etc/vmagent/config.yaml",
			"sudo: unable to resolve host x86-2204: Name or service not known",
			"",
		}, "\n")), nil).
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

func TestRemoteSHA256FromOutput(t *testing.T) {
	firstHash := strings.Repeat("a", 64)
	secondHash := strings.Repeat("b", 64)

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "clean hash",
			output: firstHash + "\n",
			want:   firstHash,
		},
		{
			name:   "sha256sum default output",
			output: firstHash + "  /etc/neutree/config.yaml\n",
			want:   firstHash,
		},
		{
			name:   "warning before hash",
			output: "sudo: unable to resolve host x86-2204: Name or service not known\n" + firstHash + "\n",
			want:   firstHash,
		},
		{
			name:   "warning after hash",
			output: firstHash + "\nsudo: unable to resolve host x86-2204: Name or service not known\n",
			want:   firstHash,
		},
		{
			name:   "uppercase hash is normalized",
			output: strings.ToUpper(firstHash) + "\n",
			want:   firstHash,
		},
		{
			name:   "last valid hash wins",
			output: firstHash + "\n" + secondHash + "\n",
			want:   secondHash,
		},
		{
			name:   "hash-looking path does not override line hash",
			output: firstHash + "  /etc/neutree/" + secondHash + "\n",
			want:   firstHash,
		},
		{
			name:   "warning only",
			output: "sudo: unable to resolve host x86-2204: Name or service not known\n",
			want:   "",
		},
		{
			name:   "truncated hash",
			output: strings.Repeat("c", 63) + "\n",
			want:   "",
		},
		{
			name:   "same length non-hex token",
			output: strings.Repeat("g", 64) + "\n",
			want:   "",
		},
		{
			name:   "empty output",
			output: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, remoteSHA256FromOutput(tt.output))
		})
	}
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

	err := (&sshFileClient{runner: runner}).writeFile(
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

func TestSSHFileClient_WriteFileUsesRemotePathInTempName(t *testing.T) {
	content := []byte("same config")
	firstPath := uploadedRemoteTempPath(t, "/etc/neutree/a.yaml", content)
	secondPath := uploadedRemoteTempPath(t, "/etc/neutree/b.yaml", content)

	assert.NotEqual(t, firstPath, secondPath)
	assert.Contains(t, firstPath, "/tmp/neutree-remote-file-"+sha256Hex(content)[:16]+"-")
	assert.Contains(t, secondPath, "/tmp/neutree-remote-file-"+sha256Hex(content)[:16]+"-")
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

func uploadedRemoteTempPath(t *testing.T, remotePath string, content []byte) string {
	t.Helper()

	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)
	uploadedPath := ""

	mockExec.On("Execute", mock.Anything, "scp", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		prefix := testSSHUser + "@" + testSSHIP + ":"
		index := strings.Index(joined, prefix)
		if index < 0 {
			return false
		}

		uploadedPath = strings.TrimSpace(joined[index+len(prefix):])

		return strings.Contains(uploadedPath, "/tmp/neutree-remote-file-")
	})).Return([]byte(""), nil).Once()
	mockExec.On("Execute", mock.Anything, "ssh", sshArgsContaining("uptime")).
		Return([]byte("success"), nil).
		Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
		return strings.Contains(strings.Join(args, " "), remotePath)
	})).Return([]byte(""), nil).Once()

	err := (&sshFileClient{runner: runner}).writeFile(
		context.Background(),
		remotePath,
		content,
		WriteFileOptions{},
	)

	require.NoError(t, err)
	require.NotEmpty(t, uploadedPath)
	mockExec.AssertExpectations(t)

	return uploadedPath
}

func TestSSHFileClient_Remove(t *testing.T) {
	mockExec := new(commandmocks.MockExecutor)
	runner := newSSHCommandRunner(mockExec)

	mockExec.On("Execute", mock.Anything, "ssh", sshArgsContaining("uptime")).
		Return([]byte("success"), nil).
		Once()
	mockExec.On("Execute", mock.Anything, "ssh", mock.MatchedBy(func(args []string) bool {
		joined := strings.Join(args, " ")
		return strings.Contains(joined, "rm -f") &&
			strings.Contains(joined, "/etc/neutree/metrics.yaml")
	})).
		Return([]byte(""), nil).
		Once()

	err := runner.Files().Remove(
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
