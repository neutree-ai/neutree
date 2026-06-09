package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/pkg/command"
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
)

var _ clusterreconcile.StaticNodeRunnerFactory = (*StaticNodeSSHRunnerFactory)(nil)

type StaticNodeSSHRunnerFactory struct {
	ProcessExecute commandrunner.ProcessExecute
	SSHControlPath string
}

func NewStaticNodeSSHRunnerFactory() *StaticNodeSSHRunnerFactory {
	executor := &command.OSExecutor{}

	return &StaticNodeSSHRunnerFactory{
		ProcessExecute: executor.Execute,
	}
}

func (f *StaticNodeSSHRunnerFactory) NewStaticNodeRunner(
	_ context.Context,
	node *v1.StaticNode,
) (clusterreconcile.StaticNodeCommandRunner, error) {
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

	return &staticNodeSSHRunner{
		runner: commandrunner.NewSSHCommandRunner(
			staticNodeRunnerID(node),
			node.Spec.IP,
			auth,
			sshControlPath,
			processExecute,
		),
	}, nil
}

type staticNodeSSHRunner struct {
	runner *commandrunner.SSHCommandRunner
}

func (r *staticNodeSSHRunner) Run(ctx context.Context, command string) (string, error) {
	return r.runner.Run(ctx, command, false, nil, true, nil, "", false)
}

func (r *staticNodeSSHRunner) Files() commandrunner.FileClient {
	return r.runner.Files()
}

func staticNodeSSHAuth(node *v1.StaticNode) (v1.Auth, error) {
	if node.Spec.SSHAuth != nil {
		auth := *node.Spec.SSHAuth
		if strings.TrimSpace(auth.SSHUser) == "" {
			return v1.Auth{}, errors.New("static node spec.ssh_auth.ssh_user is required")
		}

		return auth, nil
	}

	if node.Spec.SSHAuthRef != "" {
		return v1.Auth{}, errors.Errorf(
			"static node spec.ssh_auth_ref %q is not resolved yet; set spec.ssh_auth for static node reconcile",
			node.Spec.SSHAuthRef,
		)
	}

	return v1.Auth{}, errors.New("static node spec.ssh_auth is required")
}

func staticNodeRunnerID(node *v1.StaticNode) string {
	if node.Metadata != nil && node.Metadata.Name != "" {
		if node.Metadata.Workspace != "" {
			return fmt.Sprintf("%s/%s", node.Metadata.Workspace, node.Metadata.Name)
		}

		return node.Metadata.Name
	}

	if node.Spec != nil {
		return node.Spec.IP
	}

	return "static-node"
}
