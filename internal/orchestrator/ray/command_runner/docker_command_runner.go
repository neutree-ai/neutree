package command_runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type DockerCommandRunner struct {
	sshCommandRunner *SSHCommandRunner
	dockerConfig     *v1.Docker
	homeDir          string
	dockerCmd        string
}

func NewDockerCommandRunner(dockerConfig *v1.Docker, sshCommandConfig *CommonArgs) *DockerCommandRunner {
	sshCommandRunner := NewSSHCommandRunner(sshCommandConfig.NodeID, sshCommandConfig.SshIP,
		sshCommandConfig.AuthConfig, sshCommandConfig.ClusterName, sshCommandConfig.ProcessExecute)

	return &DockerCommandRunner{
		sshCommandRunner: sshCommandRunner,
		dockerConfig:     dockerConfig,
		dockerCmd:        "docker",
	}
}

// Run runs a command inside the Docker container.
func (d *DockerCommandRunner) Run(ctx context.Context, cmd string, exitOnFail bool, portForward []string, withOutput bool,
	environmentVariables map[string]interface{}, runEnv string, sshOptionsOverrideSSHKey string, shutdownAfterRun bool) (string, error) {
	var (
		err error
	)

	if runEnv == "auto" {
		if cmd == "" || strings.HasPrefix(cmd, d.dockerCmd) {
			runEnv = "host"
		} else {
			runEnv = d.dockerCmd
		}
	}

	if len(environmentVariables) > 0 {
		cmd = prependEnvVars(cmd, environmentVariables)
	}

	if runEnv == d.dockerCmd {
		cmd, err = d.dockerExpandUser(ctx, cmd, true)
		if err != nil {
			return "", err
		}

		cmd = WithDockerExec([]string{cmd}, d.dockerConfig.ContainerName, d.dockerCmd, nil, false)[0]
	}

	if shutdownAfterRun {
		cmd += "; sudo shutdown -h now"
	}

	return d.sshCommandRunner.Run(ctx, cmd, exitOnFail, portForward, withOutput, environmentVariables, runEnv, sshOptionsOverrideSSHKey, false)
}

func WithDockerExec(
	cmds []string,
	containerName string,
	dockerCmd string,
	envVars []string,
	withInteractive bool,
) []string {
	var execCommands []string

	interactiveFlag := ""
	if withInteractive {
		interactiveFlag = "-it"
	}

	envStr := strings.Join(envVars, " ")

	for _, cmd := range cmds {
		// handle single quotes in command (bash escaping)
		escapedCmd := strings.ReplaceAll(cmd, "'", "'\"'\"'")

		execCmd := fmt.Sprintf(
			"%s exec %s %s %s /bin/bash -c '%s'",
			dockerCmd,
			interactiveFlag,
			strings.TrimSpace(envStr),
			containerName,
			escapedCmd,
		)
		execCommands = append(execCommands, execCmd)
	}

	return execCommands
}

// CheckContainerStatus checks if the Docker container is running.
func (d *DockerCommandRunner) CheckContainerStatus(ctx context.Context) (bool, error) {
	output, err := d.sshCommandRunner.Run(ctx, fmt.Sprintf("%s inspect -f '{{.State.Running}}' %s",
		d.dockerCmd, d.dockerConfig.ContainerName), false, nil, true, nil, "host", "", false)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such object") {
			return false, nil
		} else {
			return false, err
		}
	}

	return strings.TrimSpace(output) == "true", nil
}

// dockerExpandUser expands the ~ character in a string to the user's home directory.
func (d *DockerCommandRunner) dockerExpandUser(ctx context.Context, s string, anyChar bool) (string, error) {
	userPos := strings.Index(s, "~")
	if userPos > -1 {
		if d.homeDir == "" {
			output, err := d.sshCommandRunner.Run(ctx, fmt.Sprintf("%s exec %s printenv HOME",
				d.dockerCmd, d.dockerConfig.ContainerName), false, nil, true, nil, "host", "", false)
			if err != nil {
				return "", errors.Wrap(err, "failed to get docker home directory")
			}

			d.homeDir = strings.TrimSpace(output)
		}

		if anyChar {
			return strings.ReplaceAll(s, "~/", d.homeDir+"/"), nil
		} else if !anyChar && userPos == 0 {
			return strings.Replace(s, "~", d.homeDir, 1), nil
		}
	}

	return s, nil
}

// RunInit initializes the Docker container.
func (d *DockerCommandRunner) RunInit(ctx context.Context) (bool, error) {
	clusterImage := d.dockerConfig.Image

	installed, err := d.CheckDockerInstalled(ctx)
	if err != nil {
		return false, errors.Wrap(err, "failed to check Docker installed")
	}

	if !installed {
		return false, errors.New("install docker first")
	}

	if d.dockerConfig.PullBeforeRun {
		_, err = d.sshCommandRunner.Run(ctx, fmt.Sprintf("%s pull %s", d.dockerCmd, clusterImage), true, nil, false, nil, "host", "", false)
		if err != nil {
			return false, errors.Wrap(err, "failed to pull Docker image")
		}
	} else {
		_, err = d.sshCommandRunner.Run(ctx, fmt.Sprintf("%s image inspect %s 1> /dev/null  2>&1 || %s pull %s",
			d.dockerCmd, clusterImage, d.dockerCmd, clusterImage), true, nil, false, nil, "host", "", false)
		if err != nil {
			return false, errors.Wrap(err, "failed to check or pull Docker image")
		}
	}

	containerRunning, err := d.CheckContainerStatus(ctx)
	if err != nil {
		return false, errors.Wrap(err, "failed to check container status")
	}

	if containerRunning {
		return true, nil
	}

	if !containerRunning {
		var userDockerRunOptions []string
		userDockerRunOptions = append(userDockerRunOptions, d.dockerConfig.RunOptions...)
		userDockerRunOptions = append(userDockerRunOptions, d.dockerConfig.WorkerRunOptions...)

		userDockerRunOptions, err = d.autoConfigureShm(ctx, userDockerRunOptions)
		if err != nil {
			return false, errors.Wrap(err, "failed to auto configure shm")
		}

		userOptions, err := d.configureRuntime(ctx, userDockerRunOptions)
		if err != nil {
			return false, errors.Wrap(err, "failed to configure runtime")
		}

		startCommand := d.generateDockerStartCommand(
			clusterImage,
			d.dockerConfig.ContainerName,
			d.dockerCmd,
			userOptions,
		)

		_, err = d.sshCommandRunner.Run(ctx, startCommand, true, nil, false, nil, "host", "", false)
		if err != nil {
			return false, errors.Wrap(err, "failed to start Docker container")
		}
	}

	return true, nil
}

// CheckDockerInstalled checks if Docker is installed.
func (d *DockerCommandRunner) CheckDockerInstalled(ctx context.Context) (bool, error) {
	noExist := "NoExist"

	output, err := d.sshCommandRunner.Run(ctx, fmt.Sprintf("command -v %s || echo '%s'", d.dockerCmd, noExist), false, nil, true, nil, "host", "", false)
	if err != nil {
		return false, err
	}

	if strings.Contains(output, noExist) || !strings.Contains(output, d.dockerCmd) {
		return false, nil
	}

	return true, nil
}

// generateDockerStartCommand generates the Docker start command.
func (d *DockerCommandRunner) generateDockerStartCommand(
	image string,
	containerName string,
	dockerCmd string,
	userOptions []string,
) string {
	// process container env.
	envVars := map[string]string{
		"LC_ALL": "C.UTF-8",
		"LANG":   "C.UTF-8",
	}

	envFlags := []string{}
	for k, v := range envVars {
		envFlags = append(envFlags, fmt.Sprintf("-e %s=%s", k, v))
	}

	// generate docker run command.
	dockerRunComands := []string{
		dockerCmd,
		"run",
		"--rm",
		fmt.Sprintf("--name %s", containerName),
		"-d",
		"-it",
		strings.Join(envFlags, " "),
		strings.Join(userOptions, ""),
		"--net=host",
		image,
		"bash",
	}

	return strings.Join(dockerRunComands, " ")
}

// configureRuntime configures the Docker runtime.
func (d *DockerCommandRunner) configureRuntime(ctx context.Context, runOptions []string) ([]string, error) {
	runtimeOutput, err := d.sshCommandRunner.Run(ctx, fmt.Sprintf("%s info -f '{{.Runtimes}}' ", d.dockerCmd), true, nil, true, nil, "host", "", false)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get Docker runtimes")
	}

	if strings.Contains(runtimeOutput, "nvidia-container-runtime") {
		_, err := d.sshCommandRunner.Run(ctx, "nvidia-smi", false, nil, false, nil, "host", "", false)
		if err == nil {
			return append(runOptions, " --runtime=nvidia --gpus all "), nil
		}

		klog.Info("Nvidia Container Runtime is present, but no GPUs found.")
	}

	return runOptions, nil
}

// autoConfigureShm auto - configures the SHM size.
func (d *DockerCommandRunner) autoConfigureShm(ctx context.Context, runOptions []string) ([]string, error) {
	for _, opt := range runOptions {
		if strings.Contains(opt, "--shm-size") {
			return runOptions, nil
		}
	}

	shmOutput, err := d.sshCommandRunner.Run(ctx, "cat /proc/meminfo || true", true, nil, true, nil, "host", "", false)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get system memory info")
	}

	var availableMemory int
	for _, line := range strings.Split(shmOutput, "\n") { //nolint:wsl
		if strings.Contains(line, "MemAvailable") {
			fmt.Sscanf(line, "MemAvailable: %d kB", &availableMemory) //nolint:errcheck
			break
		}
	}

	if availableMemory == 0 {
		return nil, errors.New("failed to get available memory")
	}

	availableMemoryBytes := availableMemory * 1024
	// overestimate SHM size by 10%
	shmSize := int(float64(availableMemoryBytes) * 0.1 * 1.1)

	return append(runOptions, fmt.Sprintf("--shm-size='%db'", shmSize)), nil
}

// helper functions
func prependEnvVars(cmd string, environmentVariables map[string]interface{}) string {
	var envStrings []string
	for key, val := range environmentVariables {
		envStrings = append(envStrings, fmt.Sprintf("export %s=%v;", key, val))
	}

	return strings.Join(envStrings, "") + cmd
}
