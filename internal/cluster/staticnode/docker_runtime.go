package staticnode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"sort"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	componentReasonImagePullFailed     = "ImagePullFailed"
	componentReasonDockerConfigFailed  = "DockerConfigFailed"
	componentReasonRegistryLoginFailed = "RegistryLoginFailed"
	componentReasonRemoveFailed        = "ContainerRemoveFailed"
	DockerConfigHostDir                = "/etc/neutree/docker"
)

// DockerRuntime is the low-level Docker boundary for one static node.
// It owns Docker command construction, execution, and Docker-specific failure
// reasons. The runner is injected by the caller; this boundary does not create
// SSH sessions or own runner cleanup.
type DockerRuntime struct {
	runner       CommandRunner
	registryAuth *RegistryAuth
}

func NewDockerRuntime(ctx context.Context, runner CommandRunner, registryAuth *RegistryAuth) (DockerRuntime, error) {
	runtime := DockerRuntime{runner: runner, registryAuth: registryAuth}
	if err := runtime.EnsureDockerConfigDir(ctx); err != nil {
		return DockerRuntime{}, err
	}

	if err := runtime.EnsureRegistryAuth(ctx); err != nil {
		return DockerRuntime{}, err
	}

	return runtime, nil
}

type DockerError struct {
	Reason  string
	Message string
	Err     error
}

func (e *DockerError) Error() string {
	if e == nil {
		return ""
	}

	if e.Err == nil {
		return e.Message
	}

	return e.Message + ": " + e.Err.Error()
}

func (e *DockerError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}

func dockerReason(err error, fallback string) string {
	var dockerErr *DockerError
	if stderrors.As(err, &dockerErr) && dockerErr.Reason != "" {
		return dockerErr.Reason
	}

	return fallback
}

func dockerError(reason, message string, err error) error {
	return &DockerError{
		Reason:  reason,
		Message: message,
		Err:     err,
	}
}

func (r DockerRuntime) InspectImageDigest(
	ctx context.Context,
	imageRef string,
) (string, error) {
	output, err := r.runner.Run(ctx, "docker image inspect --format='{{index .RepoDigests 0}}' "+shellArg(imageRef))
	if err != nil {
		return "", dockerError(
			warmReasonImageInspectFailed,
			fmt.Sprintf("failed to inspect image %s", imageRef),
			err,
		)
	}

	return lastNonEmptyLine(output), nil
}

func (r DockerRuntime) PullImage(
	ctx context.Context,
	image string,
) error {
	return r.pullImage(ctx, image)
}

func (r DockerRuntime) pullImage(ctx context.Context, image string) error {
	pullCommand := "docker pull " + shellArg(image)
	if r.registryAuth.configured() {
		pullCommand = "docker --config " + shellArg(DockerConfigHostDir) + " pull " + shellArg(image)
	}

	if _, err := r.runner.Run(ctx, pullCommand); err != nil {
		return dockerError(
			componentReasonImagePullFailed,
			fmt.Sprintf("failed to pull image %s", image),
			err,
		)
	}

	return nil
}

func (r DockerRuntime) Login(ctx context.Context) error {
	if !r.registryAuth.configured() {
		return nil
	}

	command := "docker --config " + shellArg(DockerConfigHostDir) +
		" login " + shellArg(r.registryAuth.Server) +
		" -u " + shellArg(r.registryAuth.Username) +
		" -p " + shellArg(r.registryAuth.Password)

	if _, err := r.runner.Run(ctx, command); err != nil {
		return dockerError(
			componentReasonRegistryLoginFailed,
			fmt.Sprintf("failed to login docker registry %s", r.registryAuth.Server),
			redactSecret(err, r.registryAuth.Password),
		)
	}

	return nil
}

func (r DockerRuntime) EnsureRegistryAuth(ctx context.Context) error {
	if !r.registryAuth.configured() {
		return nil
	}

	if r.registryConfigMatches(ctx) {
		return nil
	}

	return r.Login(ctx)
}

func (r DockerRuntime) registryConfigMatches(ctx context.Context) bool {
	output, err := r.runner.Run(ctx, "cat "+shellArg(DockerConfigHostDir+"/config.json"))
	if err != nil {
		return false
	}

	var config dockerConfigFile
	if err := json.Unmarshal([]byte(output), &config); err != nil {
		return false
	}

	auth, ok := config.Auths[r.registryAuth.Server]
	if !ok {
		return false
	}

	return auth.Auth == expectedRegistryAuth(r.registryAuth)
}

type dockerConfigFile struct {
	Auths map[string]dockerConfigAuth `json:"auths"`
}

type dockerConfigAuth struct {
	Auth string `json:"auth"`
}

func expectedRegistryAuth(auth *RegistryAuth) string {
	if auth == nil {
		return ""
	}

	return base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password))
}

func redactSecret(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}

	return stderrors.New(strings.ReplaceAll(err.Error(), secret, "[REDACTED]"))
}

func (r DockerRuntime) EnsureDockerConfigDir(ctx context.Context) error {
	if _, err := r.runner.Run(ctx, "mkdir -p "+shellArg(DockerConfigHostDir)); err != nil {
		return dockerError(
			componentReasonDockerConfigFailed,
			fmt.Sprintf("failed to create docker config dir %s", DockerConfigHostDir),
			err,
		)
	}

	return nil
}

func (r DockerRuntime) InspectLocalImage(
	ctx context.Context,
	image string,
) error {
	if _, err := r.runner.Run(ctx, "docker image inspect "+shellArg(image)+" >/dev/null"); err != nil {
		return dockerError(
			warmReasonImageInspectFailed,
			fmt.Sprintf("failed to inspect image %s", image),
			err,
		)
	}

	return nil
}

func (r DockerRuntime) EnsureImage(
	ctx context.Context,
	image string,
) error {
	if inspectErr := r.InspectLocalImage(ctx, image); inspectErr == nil {
		return nil
	}

	if pullErr := r.pullImage(ctx, image); pullErr != nil {
		return pullErr
	}

	return nil
}

func (r DockerRuntime) ComponentContainerMatches(
	ctx context.Context,
	containerName string,
	componentHash string,
) (bool, error) {
	output, err := r.runner.Run(ctx, "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' "+shellArg(containerName))
	if err != nil {
		return false, dockerError(
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

func (r DockerRuntime) RestartComponentContainer(
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
		return dockerError(
			componentReasonRunFailed,
			fmt.Sprintf("failed to run component container %s", containerName),
			err,
		)
	}

	return nil
}

func (r DockerRuntime) RemoveContainer(
	ctx context.Context,
	containerName string,
) error {
	if _, err := r.runner.Run(ctx, "docker rm -f "+shellArg(containerName)+" >/dev/null 2>&1 || true"); err != nil {
		return dockerError(
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
