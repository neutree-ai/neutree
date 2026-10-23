package bench

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	internalutil "github.com/neutree-ai/neutree/internal/util"
)

type remoteRunner interface {
	Run(ctx context.Context, command string, stdout, stderr io.Writer) error
}

type sshRemoteRunner struct {
	auth    v1.Auth
	host    string
	keyPath string
}

func newSSHRemoteRunner(auth v1.Auth, host string) (*sshRemoteRunner, error) {
	if !safeSSHUser(auth.SSHUser) {
		return nil, fmt.Errorf("invalid SSH user")
	}

	if !safeSSHHost(host) {
		return nil, fmt.Errorf("invalid SSH host %q", host)
	}

	runner := &sshRemoteRunner{auth: auth, host: host}
	if auth.SSHPrivateKey == "" {
		return runner, nil
	}

	keyPath, err := internalutil.GenerateTmpSSHKeyFile(auth.SSHPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("create temporary SSH key: %w", err)
	}

	runner.keyPath = keyPath

	return runner, nil
}

func (r *sshRemoteRunner) Run(ctx context.Context, command string, stdout, stderr io.Writer) error {
	if r == nil {
		return fmt.Errorf("SSH runner is required")
	}

	if stdout == nil {
		stdout = io.Discard
	}

	if stderr == nil {
		stderr = io.Discard
	}

	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=3",
	}
	if r.keyPath != "" {
		args = append(args, "-i", r.keyPath)
	}

	args = append(args, sshDestination(r.auth.SSHUser, r.host), command)
	process := exec.CommandContext(ctx, "ssh", args...)
	process.Stdout = stdout
	process.Stderr = stderr

	if err := process.Run(); err != nil {
		return fmt.Errorf("run SSH command on %s: %w", r.host, err)
	}

	return nil
}

func (r *sshRemoteRunner) Close() error {
	if r == nil || r.keyPath == "" {
		return nil
	}

	return os.RemoveAll(filepath.Dir(r.keyPath))
}

type dockerRemoteRunner struct {
	ssh         *sshRemoteRunner
	containerID string
}

func (r dockerRemoteRunner) Run(ctx context.Context, command string, stdout, stderr io.Writer) error {
	if r.ssh == nil {
		return fmt.Errorf("SSH runner is required")
	}

	if !validDockerContainerID(r.containerID) {
		return fmt.Errorf("invalid Docker container ID")
	}

	dockerCommand := "docker exec " + shellQuote(r.containerID) + " sh -c " + shellQuote(command)

	return r.ssh.Run(ctx, dockerCommand, stdout, stderr)
}

func runRemoteOutput(ctx context.Context, runner remoteRunner, command string) (string, error) {
	var stdout, stderr bytes.Buffer
	if err := runner.Run(ctx, command, &stdout, &stderr); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", fmt.Errorf("%w: %s", err, detail)
		}

		return "", err
	}

	return stdout.String(), nil
}

func safeSSHUser(value string) bool {
	return value != "" && !strings.ContainsAny(value, "@/\\ \t\r\n")
}

func safeSSHHost(value string) bool {
	return value != "" && !strings.HasPrefix(value, "-") && !strings.ContainsAny(value, " \t\r\n")
}

func sshDestination(user, host string) string {
	if parsed := net.ParseIP(host); parsed != nil && strings.Contains(host, ":") {
		return user + "@[" + host + "]"
	}

	return user + "@" + host
}
