package ray

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/command_runner"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/config"
	"github.com/neutree-ai/neutree/pkg/command"
)

type ClusterManager struct {
	executor  command.Executor
	configMgr *config.Manager
	config    *v1.RayClusterConfig
}

func NewRayClusterManager(cfg *v1.RayClusterConfig,
	executor command.Executor) (*ClusterManager, error) {
	if cfg == nil {
		return nil, errors.New("cluster config cannot be nil")
	}

	configMgr := config.NewManager(cfg.ClusterName)

	if err := configMgr.Generate(cfg); err != nil {
		return nil, errors.Wrap(err, "failed to generate config")
	}

	manager := &ClusterManager{
		executor:  executor,
		configMgr: configMgr,
		config:    cfg,
	}

	return manager, nil
}

func (c *ClusterManager) DownCluster(ctx context.Context) error {
	downArgs := []string{
		"down",
		"-y",
		"-v",
	}

	downArgs = append(downArgs, c.configMgr.ConfigPath())

	output, err := c.executor.Execute(ctx, "ray", downArgs...)
	if err != nil {
		return errors.Wrap(err, "failed to down cluster: "+string(output))
	}

	klog.V(4).Infof("Ray cluster down output: %s", string(output))

	return nil
}

func (c *ClusterManager) UpCluster(ctx context.Context, restart bool) (string, error) {
	upArgs := []string{
		"up",
		"--disable-usage-stats",
		"--no-config-cache",
		"-y",
		"-v",
	}

	if !restart {
		upArgs = append(upArgs, "--no-restart")
	}

	upArgs = append(upArgs, c.configMgr.ConfigPath())

	output, err := c.executor.Execute(ctx, "ray", upArgs...)
	if err != nil {
		return "", errors.Wrap(err, "failed to up cluster: "+string(output))
	}

	klog.V(4).Infof("Ray cluster up output: %s", string(output))

	headIP, err := c.GetHeadIP(ctx)
	if err != nil {
		return "", errors.Wrap(err, "failed to get head ip")
	}

	return headIP, nil
}

func (c *ClusterManager) StartNode(ctx context.Context, nodeIP string) error {
	headIP, err := c.GetHeadIP(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get head ip")
	}

	env := map[string]interface{}{
		"RAY_HEAD_IP": headIP,
	}

	sshCommandArgs := c.buildSSHCommandArgs(nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&c.config.Docker, sshCommandArgs)

	for _, command := range c.config.InitializationCommands {
		_, err = dockerCommandRunner.Run(ctx, command, true, nil, false, env, "host", "", false)
		if err != nil {
			return errors.Wrap(err, "failed to run command "+command)
		}
	}

	succeed, err := dockerCommandRunner.RunInit(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to run docker runtime init")
	}

	if !succeed {
		return errors.New("failed to run docker runtime init")
	}

	for _, command := range c.config.StaticWorkerStartRayCommands {
		_, err = dockerCommandRunner.Run(ctx, command, true, nil, false, env, "docker", "", false)
		if err != nil {
			return errors.Wrap(err, "failed to run command "+command)
		}
	}

	return nil
}

func (c *ClusterManager) DrainNode(ctx context.Context, nodeID, reason, message string, deadlineRemainSeconds int) error {
	headIP, err := c.GetHeadIP(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get head ip")
	}

	gcsServerURL := headIP + ":6379"
	drainArgs := []string{
		"drain-node",
		"--address=" + gcsServerURL,
		"--node-id=" + nodeID,
		"--reason=" + reason,
		fmt.Sprintf(`--reason-message="%s"`, message),
		"--deadline-remaining-seconds=" + fmt.Sprintf("%d", deadlineRemainSeconds),
	}

	output, err := c.executor.Execute(ctx, "ray", drainArgs...)
	if err != nil {
		return errors.Wrap(err, "failed to drain node: "+string(output))
	}

	klog.V(4).Infof("Ray drain-node output: %s", string(output))

	return nil
}

func (c *ClusterManager) StopNode(ctx context.Context, nodeIP string) error {
	sshCommandArgs := c.buildSSHCommandArgs(nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&c.config.Docker, sshCommandArgs)

	dockerInstalled, err := dockerCommandRunner.CheckDockerInstalled(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to check docker installed")
	}

	if !dockerInstalled {
		return nil
	}

	containerRunning, err := dockerCommandRunner.CheckContainerStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to check container status")
	}

	if !containerRunning {
		return nil
	}

	_, err = dockerCommandRunner.Run(ctx, "ray stop", true, nil, false, nil, "docker", "", false)
	if err != nil {
		return errors.Wrap(err, "failed to stop ray process")
	}

	_, err = dockerCommandRunner.Run(ctx, "docker stop "+c.config.Docker.ContainerName, true, nil, false, nil, "host", "", false)
	if err != nil {
		return errors.Wrap(err, "failed to stop docker container")
	}

	return nil
}

func (c *ClusterManager) GetHeadIP(ctx context.Context) (string, error) {
	output, err := c.executor.Execute(ctx, "ray", "get-head-ip", c.configMgr.ConfigPath())
	if err != nil {
		return "", errors.Wrap(err, "failed to get cluster head ip: "+string(output))
	}

	klog.V(4).Infof("Ray get-head-ip output: %s", string(output))

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "", errors.New("empty output from ray get-head-ip command")
	}

	// last line is head ip
	lines := strings.Split(trimmed, "\n")

	return lines[len(lines)-1], nil
}

func (c *ClusterManager) buildSSHCommandArgs(nodeIP string) *command_runner.CommonArgs {
	return &command_runner.CommonArgs{
		NodeID: nodeIP,
		SshIP:  nodeIP,
		AuthConfig: v1.Auth{
			SSHUser:       c.config.Auth.SSHUser,
			SSHPrivateKey: c.configMgr.SSHKeyPath(),
		},
		ClusterName:    c.config.ClusterName,
		ProcessExecute: c.executor.Execute,
	}
}
