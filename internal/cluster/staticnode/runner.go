package staticnode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/command"
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
)

type SSHRunnerFactory struct {
	ProcessExecute commandrunner.ProcessExecute
	SSHControlPath string
}

func NewSSHRunnerFactory() *SSHRunnerFactory {
	executor := &command.OSExecutor{}

	return &SSHRunnerFactory{
		ProcessExecute: executor.Execute,
	}
}

func (f *SSHRunnerFactory) NewStaticNodeRunner(
	_ context.Context,
	node *v1.StaticNode,
) (CommandRunner, error) {
	if node == nil || node.Spec == nil {
		return nil, errors.New("static node spec is required")
	}

	if node.Spec.IP == "" {
		return nil, errors.New("static node spec.ip is required")
	}

	auth, err := staticNodeSSHAuth(node)
	if err != nil {
		return nil, err
	}

	cleanupDir := ""

	if auth.SSHPrivateKey != "" {
		sshKeyPath, err := util.GenerateTmpSSHKeyFile(auth.SSHPrivateKey)
		if err != nil {
			return nil, err
		}

		auth.SSHPrivateKey = sshKeyPath
		cleanupDir = filepath.Dir(sshKeyPath)
	}

	var processExecute commandrunner.ProcessExecute
	sshControlPath := ""

	if f != nil {
		processExecute = f.ProcessExecute
		sshControlPath = f.SSHControlPath
	}

	if processExecute == nil {
		executor := &command.OSExecutor{}
		processExecute = executor.Execute
	}

	// Default the ControlPath to the per-runner temporary key directory. Scoping
	// the multiplexed socket to the runner's own key dir keeps every reconcile
	// cycle on a fresh ControlMaster (credential rotation takes effect
	// immediately) while collapsing dozens of per-command SSH connections into
	// one within the cycle. An explicit SSHControlPath override wins when set.
	if sshControlPath == "" {
		sshControlPath = cleanupDir
	}

	// Keyless (agent-auth) nodes have no temp key dir, so cleanupDir is empty and
	// ControlMaster reuse stays off for them — each command opens a fresh
	// connection, matching the pre-existing behavior.

	return &staticNodeSSHRunner{
		runner: commandrunner.NewSSHCommandRunner(
			staticNodeRunnerID(node),
			node.Spec.IP,
			auth,
			sshControlPath,
			processExecute,
		),
		cleanupDir: cleanupDir,
	}, nil
}

type staticNodeSSHRunner struct {
	runner     *commandrunner.SSHCommandRunner
	cleanupDir string
}

func (r *staticNodeSSHRunner) Run(ctx context.Context, command string) (string, error) {
	return r.runner.Run(ctx, command, false, nil, true, nil, "", false)
}

func (r *staticNodeSSHRunner) Files() commandrunner.FileClient {
	return r.runner.Files()
}

func (r *staticNodeSSHRunner) Close() error {
	if r == nil {
		return nil
	}

	// Reap the multiplexed master (if any) before unlinking its socket dir, so a
	// ControlMaster=auto background daemon can't keep an authenticated connection
	// to the node alive for ControlPersist after the cycle ends. Best-effort: a
	// missing/stale socket is normal, and the real cleanup below is RemoveAll.
	if r.runner != nil {
		_ = r.runner.CloseControlMaster(context.Background())
	}

	if r.cleanupDir == "" {
		return nil
	}

	return os.RemoveAll(r.cleanupDir)
}

func staticNodeSSHAuth(node *v1.StaticNode) (v1.Auth, error) {
	if node.Spec.SSHAuth != nil {
		auth := *node.Spec.SSHAuth
		if strings.TrimSpace(auth.SSHUser) == "" {
			return v1.Auth{}, errors.New("static node spec.ssh_auth.ssh_user is required")
		}

		return auth, nil
	}

	return v1.Auth{}, errors.New("static node spec.ssh_auth is required")
}

func staticNodeRunnerID(node *v1.StaticNode) string {
	if node.Metadata.Workspace != "" {
		return fmt.Sprintf("%s/%s", node.Metadata.Workspace, node.Metadata.Name)
	}

	return node.Metadata.Name
}
