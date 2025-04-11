package command_runner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type ProcessExecute func(ctx context.Context, name string, args []string) ([]byte, error)

// SSHCommandRunner represents an SSH command runner.
type SSHCommandRunner struct {
	nodeID         string
	clusterName    string
	processExecute ProcessExecute
	sshPrivateKey  string
	sshUser        string
	sshControlPath string
	sshIP          string
}

type CommonArgs struct {
	NodeID         string
	SshIP          string
	AuthConfig     v1.Auth
	ClusterName    string
	ProcessExecute ProcessExecute
}

// NewSSHCommandRunner creates a new SSHCommandRunner instance.
func NewSSHCommandRunner(nodeID string, sshIP string, authConfig v1.Auth, clusterName string, processExecute ProcessExecute) *SSHCommandRunner {
	var sshControlPath string

	if clusterName != "" {
		sshControlHash := fmt.Sprintf("%x", hashString(clusterName))[:10]
		sshControlPath = fmt.Sprintf("/tmp/ray_ssh/%s", sshControlHash)
		_ = os.MkdirAll(sshControlPath, 0700) //nolint:errcheck
	}

	return &SSHCommandRunner{
		nodeID:         nodeID,
		clusterName:    clusterName,
		processExecute: processExecute,
		sshPrivateKey:  authConfig.SSHPrivateKey,
		sshUser:        authConfig.SSHUser,
		sshControlPath: sshControlPath,
		sshIP:          sshIP,
	}
}

// Run runs a command over SSH.
func (s *SSHCommandRunner) Run(ctx context.Context, cmd string, exitOnFail bool, portForward []string, withOutput bool,
	environmentVariables map[string]interface{}, runEnv string, sshOptionsOverrideSSHKey string, shutdownAfterRun bool) (string, error) {
	if shutdownAfterRun {
		cmd += "; sudo shutdown -h now"
	}

	sshCommand := []string{"ssh"}

	if portForward != nil {
		for _, pf := range portForward {
			sshCommand = append(sshCommand, "-L", pf)
		}
	}

	sshOptions := s.getSSHOptions(sshOptionsOverrideSSHKey)
	sshCommand = append(sshCommand, sshOptions...)
	sshCommand = append(sshCommand, fmt.Sprintf("%s@%s", s.sshUser, s.sshIP))

	if cmd != "" {
		if environmentVariables != nil {
			cmd = prependEnvVars(cmd, environmentVariables)
		}
	} else {
		cmd = "while true; do sleep 86400; done"
	}

	sshCommand = append(sshCommand, cmd)

	klog.V(4).Infof("Node %s running `%s`", s.nodeID, cmd)
	klog.V(4).Infof("Node %s running full command is `%s`", s.nodeID, strings.Join(sshCommand, " "))

	output, err := s.processExecute(ctx, sshCommand[0], sshCommand[1:])
	if err != nil {
		if exitOnFail {
			return "", fmt.Errorf("command failed:\n\n  %s\n", strings.Join(sshCommand, " "))
		}

		failMsg := "SSH command failed."
		if len(output) > 0 {
			failMsg += "\n" + string(output)
			failMsg += "\nSee above for the output from the failure."
		}

		return "", errors.New(failMsg)
	}

	if withOutput {
		return string(output), nil
	}

	return "", nil
}

func (s *SSHCommandRunner) getSSHOptions(sshOptionsOverrideSSHKey string) []string {
	sshKey := s.sshPrivateKey
	if sshOptionsOverrideSSHKey != "" {
		sshKey = sshOptionsOverrideSSHKey
	}

	sshOptions := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=3",
	}

	if s.sshControlPath != "" {
		sshOptions = append(sshOptions,
			"-o", "ControlMaster=auto",
			"-o", fmt.Sprintf("ControlPath=%s/%%C", s.sshControlPath),
			"-o", "ControlPersist=10s",
		)
	}

	if sshKey != "" {
		sshOptions = append(sshOptions, "-i", sshKey)
	}

	return sshOptions
}

func hashString(s string) []byte {
	hash := sha256.Sum256([]byte(s))
	return hash[:]
}
