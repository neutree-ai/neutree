package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/util/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
)

const (
	warmReasonImageInspectFailed = "ImageInspectFailed"
	warmReasonImagePullFailed    = "ImagePullFailed"
	warmReasonImagePulled        = "ImagePulled"
	warmReasonImageReady         = "ImageReady"

	componentReasonConfigWriteFailed = "ConfigWriteFailed"
	componentReasonDependencyPending = "DependencyPending"
	componentReasonExternallyManaged = "ExternallyManaged"
	componentReasonHealthCheckFailed = "HealthCheckFailed"
	componentReasonInspectFailed     = "ContainerInspectFailed"
	componentReasonRunFailed         = "ContainerRunFailed"
	componentReasonRunning           = "Running"

	componentHashLabel = "neutree.ai/component-hash"
	componentNameLabel = "neutree.ai/component"
	clusterNameLabel   = "neutree.ai/static-node-cluster"
)

type StaticNodeCommandRunner interface {
	Run(ctx context.Context, command string) (string, error)
}

type staticNodeFileRunner interface {
	Files() commandrunner.FileClient
}

type StaticNodeReconciler struct{}

type StaticNodeReconcileResult struct {
	Warm       *v1.WarmStatus
	Components []v1.NodeComponentStatus
}

func (r *StaticNodeReconciler) Reconcile(
	ctx context.Context,
	node *v1.StaticNode,
	runner StaticNodeCommandRunner,
) (*StaticNodeReconcileResult, error) {
	warmStatus, err := r.ReconcileWarmImages(ctx, node, runner)
	if err != nil {
		return &StaticNodeReconcileResult{Warm: warmStatus}, err
	}

	componentStatuses, err := r.ReconcileComponents(ctx, node, runner)

	return &StaticNodeReconcileResult{
		Warm:       warmStatus,
		Components: componentStatuses,
	}, err
}

func (r *StaticNodeReconciler) ReconcileWarmImages(
	ctx context.Context,
	node *v1.StaticNode,
	runner StaticNodeCommandRunner,
) (*v1.WarmStatus, error) {
	if node == nil || node.Spec == nil || node.Spec.Warm == nil || len(node.Spec.Warm.Images) == 0 {
		return &v1.WarmStatus{Ready: true}, nil
	}

	if runner == nil {
		return nil, errors.New("static node command runner is required")
	}

	status := &v1.WarmStatus{
		Ready:  true,
		Images: make([]v1.WarmImageStatus, 0, len(node.Spec.Warm.Images)),
	}

	for _, image := range node.Spec.Warm.Images {
		imageStatus, err := r.reconcileWarmImage(ctx, image, runner)
		status.Images = append(status.Images, imageStatus)

		if err != nil && image.Required {
			status.Ready = false
			status.Reason = imageStatus.Reason
			status.Message = imageStatus.Message

			return status, err
		}

		if image.Required && !imageStatus.Ready {
			status.Ready = false
		}
	}

	return status, nil
}

func (r *StaticNodeReconciler) ReconcileComponents(
	ctx context.Context,
	node *v1.StaticNode,
	runner StaticNodeCommandRunner,
) ([]v1.NodeComponentStatus, error) {
	if node == nil || node.Spec == nil || len(node.Spec.Components) == 0 {
		return nil, nil
	}

	if runner == nil {
		return nil, errors.New("static node command runner is required")
	}

	statuses := make([]v1.NodeComponentStatus, 0, len(node.Spec.Components))
	readyByName := map[string]bool{}
	errs := []error{}

	for _, component := range node.Spec.Components {
		status, err := r.reconcileComponent(ctx, node, component, readyByName, runner)
		statuses = append(statuses, status)
		readyByName[component.Name] = status.Ready

		if err != nil {
			errs = append(errs, err)
		}
	}

	return statuses, apierrors.NewAggregate(errs)
}

func (r *StaticNodeReconciler) reconcileComponent(
	ctx context.Context,
	node *v1.StaticNode,
	component v1.NodeComponentSpec,
	readyByName map[string]bool,
	runner StaticNodeCommandRunner,
) (v1.NodeComponentStatus, error) {
	status := v1.NodeComponentStatus{
		Name:          component.Name,
		Type:          component.Type,
		ObservedHash:  componentHash(component),
		ObservedImage: component.Image,
	}

	for _, dependency := range component.Dependencies {
		if !readyByName[dependency] {
			status.Phase = v1.NodeComponentPhasePending
			status.Reason = componentReasonDependencyPending
			status.Message = fmt.Sprintf("dependency %s is not ready", dependency)

			return status, nil
		}
	}

	if component.Image == "" {
		status.Ready = true
		status.Phase = v1.NodeComponentPhaseRunning
		status.Reason = componentReasonExternallyManaged

		return status, nil
	}

	configChanged, err := writeComponentConfigFiles(ctx, runner, component)
	if err != nil {
		status.Phase = v1.NodeComponentPhaseFailed
		status.Reason = componentReasonConfigWriteFailed
		status.Message = err.Error()

		return status, err
	}

	running, err := componentContainerMatches(ctx, runner, componentContainerName(node, component), status.ObservedHash)
	if err != nil {
		status.Phase = v1.NodeComponentPhaseStarting
		status.Reason = componentReasonInspectFailed
	}

	if configChanged || !running {
		if err := restartComponentContainer(ctx, runner, node, component, status.ObservedHash); err != nil {
			status.Phase = v1.NodeComponentPhaseFailed
			status.Reason = componentReasonRunFailed
			status.Message = err.Error()

			return status, err
		}
	}

	if err := checkComponentHealth(ctx, runner, component); err != nil {
		status.Phase = v1.NodeComponentPhaseDegraded
		status.Reason = componentReasonHealthCheckFailed
		status.Message = err.Error()

		return status, nil
	}

	status.Ready = true
	status.Phase = v1.NodeComponentPhaseRunning
	status.Reason = componentReasonRunning

	return status, nil
}

func writeComponentConfigFiles(
	ctx context.Context,
	runner StaticNodeCommandRunner,
	component v1.NodeComponentSpec,
) (bool, error) {
	if len(component.ConfigFiles) == 0 {
		return false, nil
	}

	fileRunner, ok := runner.(staticNodeFileRunner)
	if !ok {
		return false, errors.New("static node command runner does not support file operations")
	}

	changed := false
	files := fileRunner.Files()
	if files == nil {
		return false, errors.New("static node command runner file client is nil")
	}

	for _, configFile := range component.ConfigFiles {
		fileChanged, err := files.WriteFileIfChanged(
			ctx,
			configFile.Path,
			[]byte(configFile.Content),
			commandrunner.WriteFileOptions{
				Mode:         configFile.Mode,
				Owner:        configFile.Owner,
				Group:        configFile.Group,
				Sudo:         configFile.Sudo,
				Atomic:       configFile.Atomic,
				CreateParent: configFile.CreateParent,
			},
		)
		if err != nil {
			return changed, errors.Wrapf(err, "failed to write config file %s", configFile.Path)
		}

		changed = changed || fileChanged
	}

	return changed, nil
}

func componentContainerMatches(
	ctx context.Context,
	runner StaticNodeCommandRunner,
	containerName string,
	componentHash string,
) (bool, error) {
	output, err := runner.Run(ctx, "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' "+shellArg(containerName))
	if err != nil {
		return false, err
	}

	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) != 2 {
		return false, nil
	}

	return fields[0] == componentHash && fields[1] == "true", nil
}

func restartComponentContainer(
	ctx context.Context,
	runner StaticNodeCommandRunner,
	node *v1.StaticNode,
	component v1.NodeComponentSpec,
	componentHash string,
) error {
	if _, err := runner.Run(ctx, "docker pull "+shellArg(component.Image)); err != nil {
		return errors.Wrapf(err, "failed to pull component image %s", component.Image)
	}

	containerName := componentContainerName(node, component)
	if _, err := runner.Run(ctx, "docker rm -f "+shellArg(containerName)+" >/dev/null 2>&1 || true"); err != nil {
		return errors.Wrapf(err, "failed to remove component container %s", containerName)
	}

	if _, err := runner.Run(ctx, buildDockerRunCommand(node, component, componentHash)); err != nil {
		return errors.Wrapf(err, "failed to run component container %s", containerName)
	}

	return nil
}

func checkComponentHealth(
	ctx context.Context,
	runner StaticNodeCommandRunner,
	component v1.NodeComponentSpec,
) error {
	if component.HealthCheck == nil {
		return nil
	}

	if len(component.HealthCheck.Command) > 0 {
		_, err := runner.Run(ctx, strings.Join(shellArgs(component.HealthCheck.Command), " "))

		return err
	}

	if component.HealthCheck.HTTPPath == "" || component.HealthCheck.Port == 0 {
		return nil
	}

	timeout := component.HealthCheck.TimeoutSec
	if timeout <= 0 {
		timeout = 5
	}

	_, err := runner.Run(
		ctx,
		fmt.Sprintf(
			"curl -fsS --max-time %d %s",
			timeout,
			shellArg(fmt.Sprintf("http://127.0.0.1:%d%s", component.HealthCheck.Port, component.HealthCheck.HTTPPath)),
		),
	)

	return err
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

	if restart := dockerRestartPolicy(component.RestartPolicy); restart != "" {
		parts = append(parts, "--restart", restart)
	}

	parts = append(parts, component.DockerRunOptions...)

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

func dockerRestartPolicy(policy v1.NodeComponentRestartPolicy) string {
	switch policy {
	case v1.NodeComponentRestartPolicyAlways:
		return "unless-stopped"
	case v1.NodeComponentRestartPolicyOnFailure:
		return "on-failure"
	default:
		return ""
	}
}

func usesHostNetwork(options []string) bool {
	for _, option := range options {
		if option == "--net=host" || option == "--network=host" {
			return true
		}
	}

	return false
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

func componentHash(component v1.NodeComponentSpec) string {
	if component.ConfigHash != "" {
		return component.ConfigHash
	}

	return nodeComponentConfigHash(component)
}

func shellArgs(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, shellArg(value))
	}

	return result
}

func (r *StaticNodeReconciler) reconcileWarmImage(
	ctx context.Context,
	image v1.WarmImageSpec,
	runner StaticNodeCommandRunner,
) (v1.WarmImageStatus, error) {
	status := v1.WarmImageStatus{
		Name:  image.Name,
		Ref:   image.Ref,
		Phase: v1.WarmPhasePending,
	}

	if image.Ref == "" {
		status.Phase = v1.WarmPhaseFailed
		status.Reason = warmReasonImageInspectFailed
		status.Message = "warm image ref is required"

		return status, errors.New(status.Message)
	}

	digest, err := inspectDockerImage(ctx, runner, image.Ref)
	if err == nil && digest != "" {
		status.Ready = true
		status.Digest = digest
		status.Phase = v1.WarmPhaseReady
		status.Reason = warmReasonImageReady

		return status, nil
	}

	status.Phase = v1.WarmPhasePulling
	if _, pullErr := runner.Run(ctx, "docker pull "+shellArg(image.Ref)); pullErr != nil {
		status.Phase = v1.WarmPhaseFailed
		status.Reason = warmReasonImagePullFailed
		status.Message = fmt.Sprintf("failed to pull image %s: %v", image.Ref, pullErr)

		return status, pullErr
	}

	digest, err = inspectDockerImage(ctx, runner, image.Ref)
	if err != nil || digest == "" {
		status.Phase = v1.WarmPhaseFailed
		status.Reason = warmReasonImageInspectFailed
		status.Message = fmt.Sprintf("failed to inspect image %s after pull", image.Ref)

		if err != nil {
			status.Message += ": " + err.Error()
		}

		return status, errors.New(status.Message)
	}

	status.Ready = true
	status.Digest = digest
	status.Phase = v1.WarmPhaseReady
	status.Reason = warmReasonImagePulled

	return status, nil
}

func inspectDockerImage(ctx context.Context, runner StaticNodeCommandRunner, imageRef string) (string, error) {
	output, err := runner.Run(ctx, "docker image inspect --format='{{index .RepoDigests 0}}' "+shellArg(imageRef))
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(output), nil
}

func shellArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
