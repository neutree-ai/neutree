package bench

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type outputRunner struct {
	stdout string
	stderr string
	err    error
}

func (r outputRunner) Run(_ context.Context, _ string, stdout, stderr io.Writer) error {
	_, _ = io.WriteString(stdout, r.stdout)
	_, _ = io.WriteString(stderr, r.stderr)

	return r.err
}

func TestNewSSHRemoteRunnerValidatesWritesAndCleansKey(t *testing.T) {
	_, err := newSSHRemoteRunner(v1.Auth{SSHUser: "bad user"}, "10.0.0.1")
	require.ErrorContains(t, err, "invalid SSH user")

	_, err = newSSHRemoteRunner(v1.Auth{SSHUser: "root"}, "-unsafe")
	require.ErrorContains(t, err, "invalid SSH host")

	_, err = newSSHRemoteRunner(v1.Auth{SSHUser: "root", SSHPrivateKey: "not-base64"}, "10.0.0.1")
	require.ErrorContains(t, err, "temporary SSH key")

	encodedKey := base64.StdEncoding.EncodeToString([]byte("private-key"))
	runner, err := newSSHRemoteRunner(v1.Auth{SSHUser: "root", SSHPrivateKey: encodedKey}, "10.0.0.1")
	require.NoError(t, err)
	require.FileExists(t, runner.keyPath)

	key, err := os.ReadFile(runner.keyPath)
	require.NoError(t, err)
	require.Equal(t, "private-key", string(key))

	keyDirectory := filepath.Dir(runner.keyPath)
	require.NoError(t, runner.Close())
	require.NoDirExists(t, keyDirectory)

	withoutKey, err := newSSHRemoteRunner(v1.Auth{SSHUser: "root"}, "10.0.0.1")
	require.NoError(t, err)
	require.Empty(t, withoutKey.keyPath)
}

func TestSSHAndDockerRemoteRunnersExecuteQuotedCommands(t *testing.T) {
	sshDirectory := t.TempDir()
	sshProgram := filepath.Join(sshDirectory, "ssh")
	sshScript := `#!/bin/sh
last=""
batch_mode=no
for value in "$@"; do
  last="$value"
  if [ "$value" = "BatchMode=yes" ]; then batch_mode=yes; fi
done
if [ "$batch_mode" != "yes" ]; then exit 3; fi
if [ "$last" = "fail" ]; then
  echo failed >&2
  exit 2
fi
printf '%s\n' "$last"
`
	require.NoError(t, os.WriteFile(sshProgram, []byte(sshScript), 0700))
	t.Setenv("PATH", sshDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	sshRunner, err := newSSHRemoteRunner(v1.Auth{SSHUser: "root"}, "10.0.0.1")
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	require.NoError(t, sshRunner.Run(context.Background(), "echo hello", &stdout, &stderr))
	require.Contains(t, stdout.String(), "echo hello")

	stdout.Reset()
	stderr.Reset()
	err = sshRunner.Run(context.Background(), "fail", &stdout, &stderr)
	require.ErrorContains(t, err, "run SSH command")
	require.Contains(t, stderr.String(), "failed")

	containerID := strings.Repeat("a", 64)
	dockerRunner := dockerRemoteRunner{ssh: sshRunner, containerID: containerID}
	stdout.Reset()
	stderr.Reset()
	require.NoError(t, dockerRunner.Run(context.Background(), "echo 'hello'", &stdout, &stderr))
	require.Contains(t, stdout.String(), "docker exec '"+containerID+"' sh -c 'echo '\"'\"'hello'\"'\"''")

	err = (dockerRemoteRunner{ssh: sshRunner, containerID: "not-an-id"}).Run(context.Background(), "echo hello", nil, nil)
	require.ErrorContains(t, err, "invalid Docker container ID")
	require.ErrorContains(t, (dockerRemoteRunner{}).Run(context.Background(), "echo hello", nil, nil), "SSH runner")
}

func TestRunRemoteOutputAndSSHHelpers(t *testing.T) {
	output, err := runRemoteOutput(context.Background(), outputRunner{stdout: "ok\n"}, "ignored")
	require.NoError(t, err)
	require.Equal(t, "ok\n", output)

	_, err = runRemoteOutput(context.Background(), outputRunner{stderr: "details", err: errors.New("boom")}, "ignored")
	require.ErrorContains(t, err, "boom: details")

	require.True(t, safeSSHUser("root"))
	require.False(t, safeSSHUser("root@example"))
	require.True(t, safeSSHHost("10.0.0.1"))
	require.False(t, safeSSHHost("host name"))
	require.Equal(t, "root@[2001:db8::1]", sshDestination("root", "2001:db8::1"))
	require.Equal(t, "root@node-a", sshDestination("root", "node-a"))
}
