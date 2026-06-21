package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/util/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
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
	componentReasonHeadPending       = "HeadNodePending"
	componentReasonHealthCheckFailed = "HealthCheckFailed"
	componentReasonInspectFailed     = "ContainerInspectFailed"
	componentReasonRunFailed         = "ContainerRunFailed"
	componentReasonRunning           = "Running"
	componentReasonStopped           = "Stopped"

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

type StaticNodeReconciler struct {
	AcceleratorManager  StaticNodeAcceleratorManager
	HeadReadyChecker    StaticNodeHeadReadyChecker
	NewDashboardService dashboard.NewDashboardServiceFunc
	NodeSnapshotClient  StaticNodeSnapshotClient
	NodeSnapshotToken   string
}

type StaticNodeReconcileResult struct {
	Accelerator *v1.StaticNodeAcceleratorStatus
	Allocations []v1.StaticNodeAllocationStatus
	Warm        *v1.WarmStatus
	Components  []v1.NodeComponentStatus
}

type StaticNodeAcceleratorManager interface {
	DetectAccelerator(ctx context.Context, runner accelerator.NodeCommandRunner) (*v1.StaticNodeAcceleratorStatus, error)
}

type StaticNodeHeadReadyChecker interface {
	HeadReady(ctx context.Context, node *v1.StaticNode) (bool, error)
}

type StaticNodeHeadReadyReader interface {
	ListStaticNodes(ctx context.Context, workspace, clusterName string) ([]*v1.StaticNode, error)
}

type StaticNodeSnapshot struct {
	Accelerator v1.StaticNodeAcceleratorStatus  `json:"accelerator,omitempty"`
	Allocations []v1.StaticNodeAllocationStatus `json:"allocations,omitempty"`
}

type StaticNodeSnapshotClient interface {
	Snapshot(ctx context.Context, node *v1.StaticNode) (*StaticNodeSnapshot, error)
}

type StaticNodeHTTPSnapshotClient struct {
	Token      string
	HTTPClient *http.Client
}

type StaticNodeStoreHeadReadyChecker struct {
	Reader StaticNodeHeadReadyReader
}

func (c *StaticNodeStoreHeadReadyChecker) HeadReady(ctx context.Context, node *v1.StaticNode) (bool, error) {
	if node == nil || node.Spec == nil || node.Spec.Role != v1.StaticNodeRoleWorker {
		return true, nil
	}

	if c == nil || c.Reader == nil {
		return false, nil
	}

	workspace := ""
	if node.Metadata != nil {
		workspace = node.Metadata.Workspace
	}

	nodes, err := c.Reader.ListStaticNodes(ctx, workspace, node.Spec.Cluster)
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

func (r *StaticNodeReconciler) Reconcile(
	ctx context.Context,
	node *v1.StaticNode,
	runner StaticNodeCommandRunner,
) (*StaticNodeReconcileResult, error) {
	acceleratorStatus, err := r.ReconcileAccelerator(ctx, node, runner)
	if err != nil {
		return &StaticNodeReconcileResult{Accelerator: acceleratorStatus}, err
	}

	warmStatus, err := r.ReconcileWarmImages(ctx, node, runner)
	if err != nil {
		return &StaticNodeReconcileResult{Accelerator: acceleratorStatus, Warm: warmStatus}, err
	}

	componentStatuses, err := r.ReconcileComponents(ctx, node, runner)
	if err != nil {
		return &StaticNodeReconcileResult{
			Accelerator: acceleratorStatus,
			Warm:        warmStatus,
			Components:  componentStatuses,
		}, err
	}

	acceleratorStatus, allocations, err := r.ReconcileNodeSnapshot(
		ctx,
		node,
		acceleratorStatus,
		componentStatuses,
	)

	return &StaticNodeReconcileResult{
		Accelerator: acceleratorStatus,
		Allocations: allocations,
		Warm:        warmStatus,
		Components:  componentStatuses,
	}, err
}

func (r *StaticNodeReconciler) ReconcileAccelerator(
	ctx context.Context,
	node *v1.StaticNode,
	runner StaticNodeCommandRunner,
) (*v1.StaticNodeAcceleratorStatus, error) {
	if r == nil || r.AcceleratorManager == nil {
		if node != nil && node.Status != nil && node.Status.Accelerator != nil {
			return node.Status.Accelerator, nil
		}

		cpu := v1.CPUStaticNodeAcceleratorStatus()

		return &cpu, nil
	}

	if runner == nil {
		return nil, errors.New("static node command runner is required for accelerator discovery")
	}

	return r.AcceleratorManager.DetectAccelerator(ctx, runner)
}

func (r *StaticNodeReconciler) ReconcileNodeSnapshot(
	ctx context.Context,
	node *v1.StaticNode,
	fallback *v1.StaticNodeAcceleratorStatus,
	componentStatuses ...[]v1.NodeComponentStatus,
) (*v1.StaticNodeAcceleratorStatus, []v1.StaticNodeAllocationStatus, error) {
	if !nodeAgentReady(componentStatuses...) {
		return fallback, currentStaticNodeAllocations(node), nil
	}

	client := r.nodeSnapshotClient()
	if client == nil {
		return fallback, currentStaticNodeAllocations(node), nil
	}

	snapshot, err := client.Snapshot(ctx, node)
	if err != nil {
		return fallback, currentStaticNodeAllocations(node), err
	}
	if snapshot == nil {
		return fallback, currentStaticNodeAllocations(node), nil
	}

	accelerator := snapshot.Accelerator
	if accelerator.Type == "" {
		return fallback, snapshot.Allocations, nil
	}

	return &accelerator, snapshot.Allocations, nil
}

func (r *StaticNodeReconciler) nodeSnapshotClient() StaticNodeSnapshotClient {
	if r != nil && r.NodeSnapshotClient != nil {
		return r.NodeSnapshotClient
	}

	token := ""
	if r != nil {
		token = r.NodeSnapshotToken
	}

	return StaticNodeHTTPSnapshotClient{Token: token}
}

func nodeAgentReady(statusGroups ...[]v1.NodeComponentStatus) bool {
	if len(statusGroups) == 0 {
		return true
	}

	for _, statuses := range statusGroups {
		for _, status := range statuses {
			if status.Name == nodeAgentComponentName || status.Type == v1.NodeComponentTypeNodeAgent {
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

func (c StaticNodeHTTPSnapshotClient) Snapshot(ctx context.Context, node *v1.StaticNode) (*StaticNodeSnapshot, error) {
	if node == nil || node.Spec == nil || node.Spec.IP == "" {
		return nil, errors.New("static node ip is required for node snapshot")
	}

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, staticNodeSnapshotURL(node), nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("node snapshot %s returned %s", request.URL.String(), response.Status)
	}

	var snapshot StaticNodeSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		return nil, err
	}

	return &snapshot, nil
}

func staticNodeSnapshotURL(node *v1.StaticNode) string {
	return fmt.Sprintf("http://%s:%d/v1/node/snapshot", node.Spec.IP, defaultNodeAgentPort)
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

func (r *StaticNodeReconciler) Delete(
	ctx context.Context,
	node *v1.StaticNode,
	runner StaticNodeCommandRunner,
) error {
	if node == nil || node.Spec == nil {
		return nil
	}

	if runner == nil {
		return errors.New("static node command runner is required")
	}

	errs := []error{}

	for _, component := range node.Spec.Components {
		containerName := componentContainerName(node, component)
		if _, err := runner.Run(ctx, "docker rm -f "+shellArg(containerName)+" >/dev/null 2>&1 || true"); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to remove component container %s", containerName))
		}

		if err := removeComponentConfigFiles(ctx, runner, component); err != nil {
			errs = append(errs, err)
		}
	}

	return apierrors.NewAggregate(errs)
}

func removeComponentConfigFiles(
	ctx context.Context,
	runner StaticNodeCommandRunner,
	component v1.NodeComponentSpec,
) error {
	if len(component.ConfigFiles) == 0 {
		return nil
	}

	fileRunner, ok := runner.(staticNodeFileRunner)
	if !ok {
		return errors.New("static node command runner does not support file operations")
	}

	files := fileRunner.Files()
	if files == nil {
		return errors.New("static node command runner file client is nil")
	}

	errs := []error{}

	for _, configFile := range component.ConfigFiles {
		if err := files.Remove(ctx, configFile.Path, commandrunner.RemoveFileOptions{Sudo: configFile.Sudo}); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to remove config file %s", configFile.Path))
		}
	}

	return apierrors.NewAggregate(errs)
}

func (r *StaticNodeReconciler) ReconcileComponents(
	ctx context.Context,
	node *v1.StaticNode,
	runner StaticNodeCommandRunner,
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
	readyByName := map[string]bool{}
	errs := []error{}

	staleStatuses, staleErrs := stopStaleComponents(ctx, node, runner)
	statuses = append(statuses, staleStatuses...)
	errs = append(errs, staleErrs...)

	for _, component := range node.Spec.Components {
		status, err := r.reconcileComponent(ctx, node, component, node.Spec.Components, readyByName, runner)
		statuses = append(statuses, status)
		readyByName[component.Name] = status.Ready

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

func (r *StaticNodeReconciler) reconcileComponent(
	ctx context.Context,
	node *v1.StaticNode,
	component v1.NodeComponentSpec,
	desiredComponents []v1.NodeComponentSpec,
	readyByName map[string]bool,
	runner StaticNodeCommandRunner,
) (v1.NodeComponentStatus, error) {
	status := v1.NodeComponentStatus{
		Name:          component.Name,
		Type:          component.Type,
		ObservedHash:  componentHash(component),
		ObservedImage: component.Image,
	}

	waitForHead, err := r.shouldWaitForHead(ctx, node, component)
	if err != nil || waitForHead {
		status.Phase = v1.NodeComponentPhasePending
		status.Reason = componentReasonHeadPending
		status.Message = "head static node is not ready"

		if err != nil {
			status.Message = err.Error()
		}

		return status, nil
	}

	for _, dependency := range implicitComponentDependencies(component, desiredComponents) {
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

	if err := r.checkComponentHealth(ctx, node, runner, component); err != nil {
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

func stopStaleComponents(
	ctx context.Context,
	node *v1.StaticNode,
	runner StaticNodeCommandRunner,
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

	for _, current := range node.Status.Components {
		if current.Name == "" {
			continue
		}

		if _, ok := desired[current.Name]; ok {
			continue
		}

		status := v1.NodeComponentStatus{
			Name:   current.Name,
			Type:   current.Type,
			Phase:  v1.NodeComponentPhaseStopped,
			Reason: componentReasonStopped,
		}
		component := v1.NodeComponentSpec{Name: current.Name, Type: current.Type}

		if err := stopComponentContainer(ctx, runner, componentContainerName(node, component)); err != nil {
			status.Phase = v1.NodeComponentPhaseFailed
			status.Reason = componentReasonRunFailed
			status.Message = err.Error()
			errs = append(errs, err)
		}

		statuses = append(statuses, status)
	}

	return statuses, errs
}

func implicitComponentDependencies(component v1.NodeComponentSpec, desiredComponents []v1.NodeComponentSpec) []string {
	dependencies := []string{}

	for _, desired := range desiredComponents {
		if desired.Name == "" || desired.Name == component.Name {
			continue
		}

		switch {
		case component.Type == v1.NodeComponentTypeNodeAgent || component.Name == nodeAgentComponentName:
			if desired.Type == v1.NodeComponentTypeNodeExporter || desired.Type == v1.NodeComponentTypeAcceleratorExporter {
				dependencies = append(dependencies, desired.Name)
			}
		case component.Type == v1.NodeComponentTypeMetricsAgent || component.Name == vmagentComponentName:
			switch desired.Type {
			case v1.NodeComponentTypeNodeExporter, v1.NodeComponentTypeAcceleratorExporter, v1.NodeComponentTypeNodeAgent:
				dependencies = append(dependencies, desired.Name)
			}
		}
	}

	return dependencies
}

func (r *StaticNodeReconciler) shouldWaitForHead(
	ctx context.Context,
	node *v1.StaticNode,
	component v1.NodeComponentSpec,
) (bool, error) {
	if node == nil || node.Spec == nil || node.Spec.Role != v1.StaticNodeRoleWorker {
		return false, nil
	}

	if component.Type != v1.NodeComponentTypeRayWorker {
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

		if fileChanged && !configFile.SkipRestartOnChange {
			changed = true
		}
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

	fields := strings.Fields(lastNonEmptyLine(output))
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
	if err := ensureComponentImage(ctx, runner, component.Image); err != nil {
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

func stopComponentContainer(
	ctx context.Context,
	runner StaticNodeCommandRunner,
	containerName string,
) error {
	if _, err := runner.Run(ctx, "docker rm -f "+shellArg(containerName)+" >/dev/null 2>&1 || true"); err != nil {
		return errors.Wrapf(err, "failed to remove component container %s", containerName)
	}

	return nil
}

func ensureComponentImage(ctx context.Context, runner StaticNodeCommandRunner, image string) error {
	if _, err := runner.Run(ctx, "docker pull "+shellArg(image)); err == nil {
		return nil
	} else if _, inspectErr := runner.Run(ctx, "docker image inspect "+shellArg(image)+" >/dev/null"); inspectErr != nil {
		return err
	}

	return nil
}

func (r *StaticNodeReconciler) checkComponentHealth(
	ctx context.Context,
	node *v1.StaticNode,
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

	if component.HealthCheck.Port == 0 {
		return nil
	}

	timeout := component.HealthCheck.TimeoutSec
	if timeout <= 0 {
		timeout = 5
	}

	dashboardURL := componentHealthBaseURL(node, component.HealthCheck)

	switch component.Type {
	case v1.NodeComponentTypeRayHead:
		return r.checkRayHeadDashboardHealth(dashboardURL, staticNodeHealthHost(node), component.HealthCheck.RayNodeLabels)
	case v1.NodeComponentTypeRayWorker:
		return r.checkRayWorkerDashboardHealth(dashboardURL, staticNodeHealthHost(node), component.HealthCheck.RayNodeLabels)
	}

	if component.HealthCheck.HTTPPath == "" {
		return nil
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

func (r *StaticNodeReconciler) checkRayHeadDashboardHealth(
	dashboardURL string,
	nodeIP string,
	expectedLabels map[string]string,
) error {
	if len(expectedLabels) == 0 {
		_, err := r.dashboardService(dashboardURL).GetClusterMetadata()

		return err
	}

	return r.checkRayDashboardNodeHealth(dashboardURL, nodeIP, expectedLabels)
}

func (r *StaticNodeReconciler) checkRayWorkerDashboardHealth(
	dashboardURL string,
	nodeIP string,
	expectedLabels map[string]string,
) error {
	return r.checkRayDashboardNodeHealth(dashboardURL, nodeIP, expectedLabels)
}

func (r *StaticNodeReconciler) checkRayDashboardNodeHealth(
	dashboardURL string,
	nodeIP string,
	expectedLabels map[string]string,
) error {
	nodes, err := r.dashboardService(dashboardURL).ListNodes()
	if err != nil {
		return errors.Wrap(err, "failed to list ray dashboard nodes")
	}

	for _, node := range nodes {
		if node.IP != nodeIP || node.Raylet.State != v1.AliveNodeState {
			continue
		}

		if err := validateRayNodeLabels(node.Raylet.Labels, expectedLabels); err != nil {
			return fmt.Errorf("ray node %s %w", nodeIP, err)
		}

		return nil
	}

	return fmt.Errorf("ray node %s is not alive in dashboard", nodeIP)
}

func validateRayNodeLabels(actual map[string]string, expected map[string]string) error {
	keys := make([]string, 0, len(expected))
	for key := range expected {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		if actual[key] != expected[key] {
			return fmt.Errorf("label %s mismatch: expected %q, got %q", key, expected[key], actual[key])
		}
	}

	return nil
}

func (r *StaticNodeReconciler) dashboardService(dashboardURL string) dashboard.DashboardService {
	if r != nil && r.NewDashboardService != nil {
		return r.NewDashboardService(dashboardURL)
	}

	return dashboard.NewDashboardService(dashboardURL)
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

	return lastNonEmptyLine(output), nil
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
