package staticnode

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/util/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/staticcomponent"
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	nodeAgentComponentName = "neutree-node-agent"
	defaultNodeAgentPort   = 19101

	warmReasonImageInspectFailed = "ImageInspectFailed"
	warmReasonImagePullFailed    = "ImagePullFailed"
	warmReasonImagePulled        = "ImagePulled"
	warmReasonImageReady         = "ImageReady"

	componentReasonConfigWriteFailed = "ConfigWriteFailed"
	componentReasonHeadPending       = "HeadNodePending"
	componentReasonHealthCheckFailed = "HealthCheckFailed"
	componentReasonImageMissing      = "ImageMissing"
	componentReasonInspectFailed     = "ContainerInspectFailed"
	componentReasonRunFailed         = "ContainerRunFailed"
	componentReasonRunning           = "Running"
	componentReasonStopped           = "Stopped"

	componentHashLabel = "neutree.ai/component-hash"
	componentNameLabel = "neutree.ai/component"
	clusterNameLabel   = "neutree.ai/static-node-cluster"
)

type CommandRunner interface {
	Run(ctx context.Context, command string) (string, error)
	Files() commandrunner.FileClient
	Close() error
}

type Reconciler struct {
	AcceleratorManager       AcceleratorManager
	HeadReadyChecker         HeadReadyChecker
	NodeDeviceSnapshotClient NodeDeviceSnapshotClient
}

type ReconcileResult struct {
	Accelerator *v1.StaticNodeAcceleratorStatus
	Allocations []v1.StaticNodeAllocationStatus
	Warm        *v1.WarmStatus
	Components  []v1.NodeComponentStatus
}

type AcceleratorManager interface {
	DetectAccelerator(ctx context.Context, nodeIP string, sshAuth v1.Auth) (*v1.StaticNodeAcceleratorStatus, error)
}

type HeadReadyChecker interface {
	HeadReady(ctx context.Context, node *v1.StaticNode) (bool, error)
}

type NodeDeviceSnapshotClient interface {
	DeviceSnapshot(ctx context.Context, node *v1.StaticNode) (*v1.NodeDeviceSnapshot, error)
}

type HTTPNodeDeviceSnapshotClient struct {
	HTTPClient *http.Client
}

type ClusterHeadReadyChecker struct {
	Storage storage.Storage
}

func (c *ClusterHeadReadyChecker) HeadReady(ctx context.Context, node *v1.StaticNode) (bool, error) {
	if node == nil || node.Spec == nil || node.Spec.Role != v1.StaticNodeRoleWorker {
		return true, nil
	}

	if c == nil || c.Storage == nil {
		return false, nil
	}

	nodes, err := ListByCluster(c.Storage, node.Metadata.Workspace, node.Spec.Cluster)
	if err != nil {
		return false, err
	}

	for _, candidate := range nodes {
		if candidate == nil || candidate.Spec == nil || candidate.Status == nil {
			continue
		}

		if candidate.Spec.Role == v1.StaticNodeRoleHead {
			return candidate.Status.Phase == v1.StaticNodePhaseReady, nil
		}
	}

	return false, nil
}

func (r *Reconciler) ReconcileAccelerator(
	ctx context.Context,
	node *v1.StaticNode,
	runner CommandRunner,
) (*v1.StaticNodeAcceleratorStatus, error) {
	if node != nil && node.Status != nil && node.Status.Accelerator != nil && node.Status.Accelerator.Type != "" {
		return node.Status.Accelerator, nil
	}

	if r == nil || r.AcceleratorManager == nil {
		cpu := v1.CPUStaticNodeAcceleratorStatus()

		return &cpu, nil
	}

	if node == nil || node.Spec == nil {
		return nil, errors.New("static node spec is required for accelerator discovery")
	}

	if node.Spec.SSHAuth == nil {
		return nil, errors.New("static node spec.ssh_auth is required for accelerator discovery")
	}

	return r.AcceleratorManager.DetectAccelerator(ctx, node.Spec.IP, *node.Spec.SSHAuth)
}

func (r *Reconciler) ReconcileNodeDeviceSnapshot(
	ctx context.Context,
	node *v1.StaticNode,
	fallback *v1.StaticNodeAcceleratorStatus,
	componentStatuses ...[]v1.NodeComponentStatus,
) (*v1.StaticNodeAcceleratorStatus, []v1.StaticNodeAllocationStatus, error) {
	if fallback != nil && fallback.Type == v1.StaticNodeAcceleratorTypeCPU {
		return fallback, currentStaticNodeAllocations(node), nil
	}

	if !nodeAgentReady(componentStatuses...) {
		return fallback, currentStaticNodeAllocations(node), nil
	}

	client := r.nodeDeviceSnapshotClient()
	if client == nil {
		return fallback, currentStaticNodeAllocations(node), nil
	}

	snapshot, err := client.DeviceSnapshot(ctx, node)
	if err != nil {
		return fallback, currentStaticNodeAllocations(node), nil
	}

	if snapshot == nil {
		return fallback, currentStaticNodeAllocations(node), nil
	}

	allocations := snapshotStaticNodeAllocations(snapshot)
	accelerator := mergeStaticNodeDeviceSnapshotAccelerator(fallback, snapshot.Accelerator)
	if accelerator.Type == "" {
		return fallback, allocations, nil
	}

	if accelerator.Type == v1.StaticNodeAcceleratorTypeCPU &&
		fallback != nil &&
		fallback.Type != "" &&
		fallback.Type != v1.StaticNodeAcceleratorTypeCPU {
		return fallback, allocations, nil
	}

	return &accelerator, allocations, nil
}

func mergeStaticNodeDeviceSnapshotAccelerator(
	fallback *v1.StaticNodeAcceleratorStatus,
	snapshot v1.StaticNodeAcceleratorStatus,
) v1.StaticNodeAcceleratorStatus {
	if fallback == nil || len(fallback.Devices) == 0 || len(snapshot.Devices) == 0 {
		return snapshot
	}

	if snapshot.Type == "" {
		return snapshot
	}

	fallbackByUUID := make(map[string]v1.StaticNodeAcceleratorDeviceStatus, len(fallback.Devices))

	for _, device := range fallback.Devices {
		if device.UUID != "" {
			fallbackByUUID[device.UUID] = device
		}
	}

	for i := range snapshot.Devices {
		fallbackDevice, ok := fallbackByUUID[snapshot.Devices[i].UUID]
		if !ok {
			continue
		}

		snapshot.Devices[i] = mergeStaticNodeDeviceSnapshotDevice(fallbackDevice, snapshot.Devices[i])
	}

	return snapshot
}

func mergeStaticNodeDeviceSnapshotDevice(
	fallback v1.StaticNodeAcceleratorDeviceStatus,
	snapshot v1.StaticNodeAcceleratorDeviceStatus,
) v1.StaticNodeAcceleratorDeviceStatus {
	if snapshot.ID == "" {
		snapshot.ID = fallback.ID
	}

	if snapshot.ProductName == "" {
		snapshot.ProductName = fallback.ProductName
	}

	if snapshot.ProductModel == "" {
		snapshot.ProductModel = fallback.ProductModel
	}

	if snapshot.MemoryMiB == 0 {
		snapshot.MemoryMiB = fallback.MemoryMiB
	}

	if snapshot.MinorNumber == nil && fallback.MinorNumber != nil {
		snapshot.MinorNumber = fallback.MinorNumber
	}

	return snapshot
}

func (r *Reconciler) nodeDeviceSnapshotClient() NodeDeviceSnapshotClient {
	if r != nil && r.NodeDeviceSnapshotClient != nil {
		return r.NodeDeviceSnapshotClient
	}

	return HTTPNodeDeviceSnapshotClient{}
}

func nodeAgentReady(statusGroups ...[]v1.NodeComponentStatus) bool {
	if len(statusGroups) == 0 {
		return true
	}

	for _, statuses := range statusGroups {
		for _, status := range statuses {
			if status.Name == nodeAgentComponentName {
				return status.Ready && status.Phase == v1.NodeComponentPhaseRunning
			}
		}
	}

	return false
}

func currentStaticNodeAllocations(node *v1.StaticNode) []v1.StaticNodeAllocationStatus {
	if node == nil || node.Status == nil {
		return nil
	}

	return append([]v1.StaticNodeAllocationStatus{}, node.Status.Allocations...)
}

func snapshotStaticNodeAllocations(snapshot *v1.NodeDeviceSnapshot) []v1.StaticNodeAllocationStatus {
	if snapshot == nil {
		return nil
	}

	if snapshot.Allocations == nil {
		return []v1.StaticNodeAllocationStatus{}
	}

	return append([]v1.StaticNodeAllocationStatus{}, snapshot.Allocations...)
}

func (c HTTPNodeDeviceSnapshotClient) DeviceSnapshot(ctx context.Context, node *v1.StaticNode) (*v1.NodeDeviceSnapshot, error) {
	if node == nil || node.Spec == nil || node.Spec.IP == "" {
		return nil, errors.New("static node ip is required for node device snapshot")
	}

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, staticNodeDeviceSnapshotURL(node), nil)
	if err != nil {
		return nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("node device snapshot %s returned %s", request.URL.String(), response.Status)
	}

	var snapshot v1.NodeDeviceSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		return nil, err
	}

	return &snapshot, nil
}

func staticNodeDeviceSnapshotURL(node *v1.StaticNode) string {
	return fmt.Sprintf("http://%s:%d/v1/node/device-snapshot", node.Spec.IP, defaultNodeAgentPort)
}

func (r *Reconciler) ReconcileWarmImages(
	ctx context.Context,
	node *v1.StaticNode,
	dockerRuntime DockerRuntime,
) (*v1.WarmStatus, error) {
	if node == nil || node.Spec == nil || node.Spec.Warm == nil || len(node.Spec.Warm.Images) == 0 {
		return &v1.WarmStatus{Ready: true}, nil
	}

	status := &v1.WarmStatus{
		Ready:  true,
		Images: make([]v1.WarmImageStatus, 0, len(node.Spec.Warm.Images)),
	}

	for _, image := range node.Spec.Warm.Images {
		imageStatus, err := r.reconcileWarmImage(ctx, image, dockerRuntime)
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

func (r *Reconciler) Delete(
	ctx context.Context,
	node *v1.StaticNode,
	runner CommandRunner,
) error {
	if node == nil || node.Spec == nil {
		return nil
	}

	if runner == nil {
		return errors.New("static node command runner is required")
	}

	errs := []error{}
	fileApplier := newStaticNodeComponentFileApplier(runner.Files())

	for _, component := range staticNodeDeleteComponents(node) {
		containerName := componentContainerName(node, component)
		if _, err := runner.Run(ctx, "docker rm -f "+shellArg(containerName)+" >/dev/null 2>&1 || true"); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to remove component container %s", containerName))
		}

		if err := fileApplier.removeComponentConfigFiles(ctx, component); err != nil {
			errs = append(errs, err)
		}
	}

	if command := removeClusterLabeledComponentContainersCommand(node); command != "" {
		if _, err := runner.Run(ctx, command); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to remove static node cluster containers %s", node.Spec.Cluster))
		}
	}

	if _, err := runner.Run(ctx, "sudo rm -rf /etc/neutree"); err != nil {
		errs = append(errs, errors.Wrap(err, "failed to remove static node Neutree data directory"))
	}

	return apierrors.NewAggregate(errs)
}

func removeClusterLabeledComponentContainersCommand(node *v1.StaticNode) string {
	if node == nil || node.Spec == nil || node.Spec.Cluster == "" {
		return ""
	}

	return "containers=$(docker ps -aq --filter label=" + shellArg(clusterNameLabel+"="+node.Spec.Cluster) +
		"); if [ -n \"$containers\" ]; then docker rm -f $containers >/dev/null 2>&1; fi"
}

func staticNodeDeleteComponents(node *v1.StaticNode) []v1.NodeComponentSpec {
	if node == nil {
		return nil
	}

	components := []v1.NodeComponentSpec{}
	seen := map[string]struct{}{}

	if node.Spec != nil {
		for _, component := range node.Spec.Components {
			components = appendStaticNodeDeleteComponent(components, seen, component)
		}
	}

	if node.Status != nil {
		for _, status := range node.Status.Components {
			if status.Name == "" {
				continue
			}

			components = appendStaticNodeDeleteComponent(components, seen, v1.NodeComponentSpec{
				Name: status.Name,
			})
		}
	}

	return components
}

func appendStaticNodeDeleteComponent(
	components []v1.NodeComponentSpec,
	seen map[string]struct{},
	component v1.NodeComponentSpec,
) []v1.NodeComponentSpec {
	key := component.Name
	if key != "" {
		if _, ok := seen[key]; ok {
			return components
		}

		seen[key] = struct{}{}
	}

	return append(components, component)
}

func (r *Reconciler) ReconcileComponents(
	ctx context.Context,
	node *v1.StaticNode,
	runner CommandRunner,
	dockerRuntime DockerRuntime,
) ([]v1.NodeComponentStatus, error) {
	if node == nil || node.Spec == nil {
		return nil, nil
	}

	if len(node.Spec.Components) == 0 && existingComponentStatusCount(node) == 0 {
		return nil, nil
	}

	if runner == nil {
		return nil, errors.New("static node command runner is required")
	}

	statuses := make([]v1.NodeComponentStatus, 0, len(node.Spec.Components)+existingComponentStatusCount(node))
	errs := []error{}
	fileApplier := newStaticNodeComponentFileApplier(runner.Files())

	staleStatuses, staleErrs := stopStaleComponents(ctx, node, runner)
	statuses = append(statuses, staleStatuses...)
	errs = append(errs, staleErrs...)

	for _, component := range node.Spec.Components {
		status, err := r.reconcileComponent(ctx, node, component, runner, fileApplier, dockerRuntime)
		statuses = append(statuses, status)

		if err != nil {
			errs = append(errs, err)
		}
	}

	return statuses, apierrors.NewAggregate(errs)
}

func existingComponentStatusCount(node *v1.StaticNode) int {
	if node == nil || node.Status == nil {
		return 0
	}

	return len(node.Status.Components)
}

func (r *Reconciler) reconcileComponent(
	ctx context.Context,
	node *v1.StaticNode,
	component v1.NodeComponentSpec,
	runner CommandRunner,
	fileApplier staticNodeComponentFileApplier,
	dockerRuntime DockerRuntime,
) (v1.NodeComponentStatus, error) {
	status := v1.NodeComponentStatus{
		Name:          component.Name,
		ObservedHash:  componentHash(component),
		ObservedImage: component.Image,
	}

	waitForHead, err := r.shouldWaitForHead(ctx, node)
	if err != nil || waitForHead {
		status.Phase = v1.NodeComponentPhasePending
		status.Reason = componentReasonHeadPending
		status.Message = "head static node is not ready"

		if err != nil {
			status.Message = err.Error()
		}

		return status, nil
	}

	if component.Image == "" {
		status.Phase = v1.NodeComponentPhaseFailed
		status.Reason = componentReasonImageMissing
		status.Message = "component image is required"

		return status, errors.New(status.Message)
	}

	configChanged, err := fileApplier.writeComponentConfigFiles(ctx, component)
	if err != nil {
		status.Phase = v1.NodeComponentPhaseFailed
		status.Reason = componentReasonConfigWriteFailed
		status.Message = err.Error()

		return status, err
	}

	running, err := dockerRuntime.ComponentContainerMatches(
		ctx,
		componentContainerName(node, component),
		status.ObservedHash,
	)
	if err != nil {
		status.Phase = v1.NodeComponentPhaseStarting
		status.Reason = dockerReason(err, componentReasonInspectFailed)
	}

	restarted := configChanged || !running
	if restarted {
		if err := dockerRuntime.RestartComponentContainer(ctx, node, component, status.ObservedHash); err != nil {
			status.Phase = v1.NodeComponentPhaseFailed
			status.Reason = dockerReason(err, componentReasonRunFailed)
			status.Message = err.Error()

			return status, err
		}
	}

	if err := r.checkComponentHealth(ctx, node, runner, component); err != nil {
		status.Phase = v1.NodeComponentPhaseStarting
		status.Reason = componentReasonHealthCheckFailed
		status.Message = err.Error()

		return status, nil
	}

	status.Ready = true
	status.Phase = v1.NodeComponentPhaseRunning
	status.Reason = componentReasonRunning

	return status, nil
}

func stopStaleComponents(
	ctx context.Context,
	node *v1.StaticNode,
	runner CommandRunner,
) ([]v1.NodeComponentStatus, []error) {
	if node == nil || node.Spec == nil || node.Status == nil || len(node.Status.Components) == 0 {
		return nil, nil
	}

	desired := map[string]struct{}{}
	for _, component := range node.Spec.Components {
		desired[component.Name] = struct{}{}
	}

	statuses := []v1.NodeComponentStatus{}
	errs := []error{}
	dockerRuntime, err := NewDockerRuntime(ctx, runner, nil)

	if err != nil {
		return statuses, []error{err}
	}

	for _, current := range node.Status.Components {
		if current.Name == "" {
			continue
		}

		if _, ok := desired[current.Name]; ok {
			continue
		}

		status := v1.NodeComponentStatus{
			Name:   current.Name,
			Phase:  v1.NodeComponentPhaseStopped,
			Reason: componentReasonStopped,
		}
		component := v1.NodeComponentSpec{Name: current.Name}

		if err := dockerRuntime.RemoveContainer(ctx, componentContainerName(node, component)); err != nil {
			status.Phase = v1.NodeComponentPhaseFailed
			status.Reason = dockerReason(err, componentReasonRunFailed)
			status.Message = err.Error()
			errs = append(errs, err)
		}

		statuses = append(statuses, status)
	}

	return statuses, errs
}

func (r *Reconciler) shouldWaitForHead(
	ctx context.Context,
	node *v1.StaticNode,
) (bool, error) {
	if node == nil || node.Spec == nil || node.Spec.Role != v1.StaticNodeRoleWorker {
		return false, nil
	}

	if r == nil || r.HeadReadyChecker == nil {
		return false, nil
	}

	headReady, err := r.HeadReadyChecker.HeadReady(ctx, node)
	if err != nil {
		return true, err
	}

	return !headReady, nil
}

func (r *Reconciler) checkComponentHealth(
	ctx context.Context,
	node *v1.StaticNode,
	runner CommandRunner,
	component v1.NodeComponentSpec,
) error {
	if component.HealthCheck == nil {
		return nil
	}

	if len(component.HealthCheck.Command) > 0 {
		_, err := runner.Run(ctx, strings.Join(shellArgs(component.HealthCheck.Command), " "))

		return err
	}

	if component.HealthCheck.Port == 0 {
		return nil
	}

	timeout := component.HealthCheck.TimeoutSec
	if timeout <= 0 {
		timeout = 5
	}

	return checkHTTPHealth(ctx, componentHealthURL(node, component.HealthCheck), timeout)
}

func componentHealthBaseURL(node *v1.StaticNode, healthCheck *v1.NodeComponentHealthCheck) string {
	host := strings.TrimSpace(healthCheck.HTTPHost)
	if host == "" {
		host = staticNodeHealthHost(node)
	}

	return "http://" + net.JoinHostPort(host, strconv.Itoa(healthCheck.Port))
}

func componentHealthURL(node *v1.StaticNode, healthCheck *v1.NodeComponentHealthCheck) string {
	path := healthCheck.HTTPPath
	if path == "" {
		path = "/"
	}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return componentHealthBaseURL(node, healthCheck) + path
}

func staticNodeHealthHost(node *v1.StaticNode) string {
	if node != nil && node.Spec != nil && node.Spec.IP != "" {
		return node.Spec.IP
	}

	return "127.0.0.1"
}

func checkHTTPHealth(ctx context.Context, url string, timeoutSec int) error {
	response, err := doHealthHTTPGet(ctx, url, timeoutSec)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("http health check %s returned %s", url, response.Status)
	}

	return nil
}

func doHealthHTTPGet(ctx context.Context, url string, timeoutSec int) (*http.Response, error) {
	if timeoutSec <= 0 {
		timeoutSec = 5
	}

	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	return client.Do(request)
}

func componentHash(component v1.NodeComponentSpec) string {
	if component.ConfigHash != "" {
		return component.ConfigHash
	}

	return staticcomponent.Hash(component)
}

func shellArgs(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, shellArg(value))
	}

	return result
}

func (r *Reconciler) reconcileWarmImage(
	ctx context.Context,
	image v1.WarmImageSpec,
	dockerRuntime DockerRuntime,
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

	digest, err := dockerRuntime.InspectImageDigest(ctx, image.Ref)
	if err == nil && digest != "" {
		status.Ready = true
		status.Digest = digest
		status.Phase = v1.WarmPhaseReady
		status.Reason = warmReasonImageReady

		return status, nil
	}

	status.Phase = v1.WarmPhasePulling
	if pullErr := dockerRuntime.PullImage(ctx, image.Ref); pullErr != nil {
		status.Phase = v1.WarmPhaseFailed
		status.Reason = warmReasonImagePullFailed
		status.Message = pullErr.Error()

		return status, pullErr
	}

	digest, err = dockerRuntime.InspectImageDigest(ctx, image.Ref)
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

func lastNonEmptyLine(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}

	return ""
}

func shellArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
