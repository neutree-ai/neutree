package cluster

import (
	"context"
	stderrors "errors"
	"fmt"
	"sort"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	componentReasonImagePullFailed = "ImagePullFailed"
	componentReasonRemoveFailed    = "ContainerRemoveFailed"
)

// StaticNodeDockerRuntime is the low-level Docker boundary for one static node.
// It owns Docker command construction, execution, and Docker-specific failure
// reasons. The runner is injected by the caller; this boundary does not create
// SSH sessions or own runner cleanup.
type StaticNodeDockerRuntime struct {
	runner StaticNodeCommandRunner
}

func NewStaticNodeDockerRuntime(runner StaticNodeCommandRunner) StaticNodeDockerRuntime {
	return StaticNodeDockerRuntime{runner: runner}
}

type StaticNodeDockerError struct {
	Reason  string
	Message string
	Err     error
}

func (e *StaticNodeDockerError) Error() string {
	if e == nil {
		return ""
	}

	if e.Err == nil {
		return e.Message
	}

	return e.Message + ": " + e.Err.Error()
}

func (e *StaticNodeDockerError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}

func staticNodeDockerReason(err error, fallback string) string {
	var dockerErr *StaticNodeDockerError
	if stderrors.As(err, &dockerErr) && dockerErr.Reason != "" {
		return dockerErr.Reason
	}

	return fallback
}

func staticNodeDockerError(reason, message string, err error) error {
	return &StaticNodeDockerError{
		Reason:  reason,
		Message: message,
		Err:     err,
	}
}

func (r StaticNodeDockerRuntime) InspectImageDigest(
	ctx context.Context,
	imageRef string,
) (string, error) {
	output, err := r.runner.Run(ctx, "docker image inspect --format='{{index .RepoDigests 0}}' "+shellArg(imageRef))
	if err != nil {
		return "", staticNodeDockerError(
			warmReasonImageInspectFailed,
			fmt.Sprintf("failed to inspect image %s", imageRef),
			err,
		)
	}

	return lastNonEmptyLine(output), nil
}

func (r StaticNodeDockerRuntime) PullImage(
	ctx context.Context,
	image string,
) error {
	if _, err := r.runner.Run(ctx, "docker pull "+shellArg(image)); err != nil {
		return staticNodeDockerError(
			componentReasonImagePullFailed,
			fmt.Sprintf("failed to pull image %s", image),
			err,
		)
	}

	return nil
}

func (r StaticNodeDockerRuntime) InspectLocalImage(
	ctx context.Context,
	image string,
) error {
	if _, err := r.runner.Run(ctx, "docker image inspect "+shellArg(image)+" >/dev/null"); err != nil {
		return staticNodeDockerError(
			warmReasonImageInspectFailed,
			fmt.Sprintf("failed to inspect image %s", image),
			err,
		)
	}

	return nil
}

func (r StaticNodeDockerRuntime) EnsureImage(
	ctx context.Context,
	image string,
) error {
	pullErr := r.PullImage(ctx, image)
	if pullErr == nil {
		return nil
	}

	if inspectErr := r.InspectLocalImage(ctx, image); inspectErr != nil {
		return pullErr
	}

	return nil
}

func (r StaticNodeDockerRuntime) ComponentContainerMatches(
	ctx context.Context,
	containerName string,
	componentHash string,
) (bool, error) {
	output, err := r.runner.Run(ctx, "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' "+shellArg(containerName))
	if err != nil {
		return false, staticNodeDockerError(
			componentReasonInspectFailed,
			fmt.Sprintf("failed to inspect component container %s", containerName),
			err,
		)
	}

	fields := strings.Fields(lastNonEmptyLine(output))
	if len(fields) != 2 {
		return false, nil
	}

	return fields[0] == componentHash && fields[1] == "true", nil
}

func (r StaticNodeDockerRuntime) RestartComponentContainer(
	ctx context.Context,
	node *v1.StaticNode,
	component v1.NodeComponentSpec,
	componentHash string,
) error {
	if err := r.EnsureImage(ctx, component.Image); err != nil {
		return err
	}

	containerName := componentContainerName(node, component)
	if err := r.RemoveContainer(ctx, containerName); err != nil {
		return err
	}

	if _, err := r.runner.Run(ctx, buildDockerRunCommand(node, component, componentHash)); err != nil {
		return staticNodeDockerError(
			componentReasonRunFailed,
			fmt.Sprintf("failed to run component container %s", containerName),
			err,
		)
	}

	return nil
}

func (r StaticNodeDockerRuntime) RemoveContainer(
	ctx context.Context,
	containerName string,
) error {
	if _, err := r.runner.Run(ctx, "docker rm -f "+shellArg(containerName)+" >/dev/null 2>&1 || true"); err != nil {
		return staticNodeDockerError(
			componentReasonRemoveFailed,
			fmt.Sprintf("failed to remove component container %s", containerName),
			err,
		)
	}

	return nil
}

func buildDockerRunCommand(node *v1.StaticNode, component v1.NodeComponentSpec, componentHash string) string {
	parts := []string{
		"docker", "run", "-d",
		"--name", shellArg(componentContainerName(node, component)),
		"--label", shellArg(componentHashLabel + "=" + componentHash),
		"--label", shellArg(componentNameLabel + "=" + component.Name),
	}

	if node != nil && node.Spec != nil && node.Spec.Cluster != "" {
		parts = append(parts, "--label", shellArg(clusterNameLabel+"="+node.Spec.Cluster))
	}

	parts = append(parts, "--restart", "unless-stopped")

	parts = append(parts, dockerRunOptionArgs(component.DockerRunOptions)...)

	if !usesHostNetwork(component.DockerRunOptions) {
		for _, port := range component.Ports {
			if port.Port == 0 {
				continue
			}

			parts = append(parts, "-p", fmt.Sprintf("%d:%d", port.Port, port.Port))
		}
	}

	envKeys := make([]string, 0, len(component.Env))
	for key := range component.Env {
		envKeys = append(envKeys, key)
	}

	sort.Strings(envKeys)

	for _, key := range envKeys {
		parts = append(parts, "-e", shellArg(key+"="+component.Env[key]))
	}

	for _, volume := range component.Volumes {
		if volume.HostPath == "" || volume.MountPath == "" {
			continue
		}

		value := volume.HostPath + ":" + volume.MountPath
		if volume.ReadOnly {
			value += ":ro"
		}

		parts = append(parts, "-v", shellArg(value))
	}

	parts = append(parts, shellArg(component.Image))
	parts = append(parts, shellArgs(component.Command)...)
	parts = append(parts, shellArgs(component.Args)...)

	return strings.Join(parts, " ")
}

func usesHostNetwork(options []string) bool {
	for _, option := range options {
		fields := strings.Fields(option)
		if len(fields) == 0 {
			continue
		}

		if fields[0] == "--net=host" || fields[0] == "--network=host" {
			return true
		}

		if len(fields) > 1 && (fields[0] == "--net" || fields[0] == "--network") && fields[1] == "host" {
			return true
		}
	}

	return false
}

func dockerRunOptionArgs(options []string) []string {
	result := []string{}
	for _, option := range options {
		result = append(result, shellArgs(strings.Fields(option))...)
	}

	return result
}

func componentContainerName(node *v1.StaticNode, component v1.NodeComponentSpec) string {
	prefix := "neutree"
	if node != nil && node.Spec != nil && node.Spec.Cluster != "" {
		prefix += "-" + node.Spec.Cluster
	}

	return sanitizeContainerName(prefix + "-" + component.Name)
}

func sanitizeContainerName(value string) string {
	var builder strings.Builder
	lastDash := false

	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)

			lastDash = false

			continue
		}

		if !lastDash {
			builder.WriteByte('-')

			lastDash = true
		}
	}

	return strings.Trim(builder.String(), "-")
}
