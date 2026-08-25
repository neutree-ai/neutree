package allocation

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/ray/rayserve"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

const (
	endpointWorkloadType = "endpoint"
	defaultProcFSRoot    = "/proc"
)

type PodResourceLister interface {
	ListPodResources(ctx context.Context) ([]adapter.PodResource, error)
}

type PodResourceListerFunc func(ctx context.Context) ([]adapter.PodResource, error)

func (f PodResourceListerFunc) ListPodResources(ctx context.Context) ([]adapter.PodResource, error) {
	return f(ctx)
}

type KubernetesAllocationProvider struct {
	Client       client.Client
	NodeName     string
	PodResources PodResourceLister
}

// KubernetesAcceleratorEvidence copies kubelet PodResources and local endpoint
// Pod metadata into the public adapter evidence model. The host intentionally
// leaves resource names, device IDs, labels, and annotations uninterpreted so
// a vendor adapter, including Ascend NPU, owns allocation semantics.
func (p KubernetesAllocationProvider) KubernetesAcceleratorEvidence(
	ctx context.Context,
) (adapter.KubernetesEvidence, error) {
	if p.Client == nil || p.NodeName == "" || p.PodResources == nil {
		return adapter.KubernetesEvidence{}, nil
	}

	podResources, err := p.PodResources.ListPodResources(ctx)
	if err != nil {
		return adapter.KubernetesEvidence{}, err
	}

	pods, err := p.localEndpointPods(ctx)
	if err != nil {
		return adapter.KubernetesEvidence{}, err
	}

	return adapter.KubernetesEvidence{
		AllocationAvailable: true,
		PodResources:        clonePodResources(podResources),
		EndpointPods:        endpointPodEvidence(pods),
	}, nil
}

func (p KubernetesAllocationProvider) localEndpointPods(ctx context.Context) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := p.Client.List(
		ctx,
		podList,
		client.MatchingFields{"spec.nodeName": p.NodeName},
		client.MatchingLabels{"app": endpointWorkloadType},
	); err != nil {
		return nil, err
	}

	pods := make([]corev1.Pod, 0)

	for _, pod := range podList.Items {
		if pod.Spec.NodeName != p.NodeName || terminalPodPhase(pod.Status.Phase) {
			continue
		}

		labels := pod.GetLabels()
		if labels["app"] != endpointWorkloadType || labels["endpoint"] == "" {
			continue
		}

		pods = append(pods, pod)
	}

	sort.SliceStable(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}

		return pods[i].Name < pods[j].Name
	})

	return pods, nil
}

// endpointPodEvidence strips Kubernetes objects down to immutable metadata the
// adapter may correlate with its own allocation protocol.
func endpointPodEvidence(pods []corev1.Pod) []adapter.EndpointPodEvidence {
	evidence := make([]adapter.EndpointPodEvidence, 0, len(pods))
	for _, pod := range pods {
		evidence = append(evidence, adapter.EndpointPodEvidence{
			Namespace:   pod.Namespace,
			Name:        pod.Name,
			UID:         string(pod.UID),
			NodeName:    pod.Spec.NodeName,
			Labels:      copyStringMap(pod.Labels),
			Annotations: copyStringMap(pod.Annotations),
		})
	}

	return evidence
}

// clonePodResources prevents an adapter from retaining or mutating host-owned
// kubelet observations across a metrics collection cycle.
func clonePodResources(input []adapter.PodResource) []adapter.PodResource {
	result := make([]adapter.PodResource, 0, len(input))

	for _, pod := range input {
		copied := adapter.PodResource{Namespace: pod.Namespace, Name: pod.Name}
		copied.Containers = make([]adapter.ContainerDevices, 0, len(pod.Containers))

		for _, container := range pod.Containers {
			copied.Containers = append(copied.Containers, adapter.ContainerDevices{
				ResourceName: container.ResourceName,
				DeviceIDs:    append([]string(nil), container.DeviceIDs...),
			})
		}

		result = append(result, copied)
	}

	return result
}

func copyStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}

	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}

	return result
}

func terminalPodPhase(phase corev1.PodPhase) bool {
	return phase == corev1.PodSucceeded || phase == corev1.PodFailed
}

type RayServeAllocationProvider struct {
	Dashboard          dashboard.DashboardService
	DashboardURL       string
	NodeIP             string
	ProcEnv            ProcessEnvReader
	ProcessDescendants ProcessDescendantReader
}

// StaticAcceleratorEvidence gathers raw Ray and local-process topology for a
// static-cluster adapter. It does not infer GPU or NPU ownership; the selected
// adapter joins this evidence with vendor exporter data using its own rules.
func (p RayServeAllocationProvider) StaticAcceleratorEvidence(
	ctx context.Context,
) (adapter.StaticEvidence, error) {
	service := p.dashboardService()
	if service == nil || p.NodeIP == "" {
		return adapter.StaticEvidence{}, nil
	}

	nodeID, err := p.rayNodeID(service)
	if err != nil || nodeID == "" {
		return adapter.StaticEvidence{}, err
	}

	applications, applicationsErr := service.GetServeApplications()
	if applicationsErr != nil {
		// Replicas establish the endpoint-to-actor identity required for a
		// complete allocation view. Let the host degrade AllocationAvailable
		// rather than publish actor-only evidence as complete.
		return adapter.StaticEvidence{}, applicationsErr
	}

	actorsResp, err := service.ListActors(
		[]dashboard.ActorFilter{{Key: "node_id", Predicate: "=", Value: nodeID}},
		true,
		0,
	)
	if err != nil {
		return adapter.StaticEvidence{}, err
	}

	actors := []dashboard.Actor{}
	if actorsResp != nil {
		actors = append(actors, actorsResp.Data.Result.Result...)
	}

	envReader := p.processEnvReader()
	descendantReader := p.processDescendantReader()
	actorProcesses := make(map[int]adapter.ProcessInfo, len(actors))

	for _, actor := range actors {
		if actor.PID <= 0 {
			continue
		}

		info, ok := p.actorProcessInfo(actor.PID, envReader, descendantReader)
		if !ok {
			continue
		}

		actorProcesses[actor.PID] = info
	}

	replicas := rayReplicasFromApplications(applications, nodeID)

	return adapter.StaticEvidence{
		// A missing Serve application response cannot distinguish an empty
		// endpoint set from unavailable allocation evidence.
		AllocationAvailable: applicationsErr == nil && applications != nil,
		RayEvidence: adapter.RayEvidence{
			Actors:         rayActorsFromDashboard(actors),
			Replicas:       replicas,
			ActorProcesses: actorProcesses,
		},
	}, nil
}

// rayActorsFromDashboard copies Ray's raw requested resources into the public
// adapter boundary. detail=true is required by the dashboard client for this
// map to be present in the state API response.
func rayActorsFromDashboard(actors []dashboard.Actor) []adapter.RayActor {
	result := make([]adapter.RayActor, 0, len(actors))

	for _, actor := range actors {
		resources := make(map[string]float64, len(actor.RequiredResources))

		for name, quantity := range actor.RequiredResources {
			resources[name] = quantity
		}

		result = append(result, adapter.RayActor{
			ActorID:           actor.ActorID,
			ClassName:         actor.ClassName,
			State:             actor.State,
			Name:              actor.Name,
			NodeID:            actor.NodeID,
			PID:               actor.PID,
			RequiredResources: resources,
			StartTime:         actor.StartTime,
			EndTime:           actor.EndTime,
		})
	}

	return result
}

// rayReplicasFromApplications projects Ray Serve's replica-to-actor topology
// into vendor-neutral evidence while preserving deterministic traversal order.
func rayReplicasFromApplications(
	applications *dashboard.RayServeApplicationsResponse,
	nodeID string,
) []adapter.RayReplica {
	if applications == nil {
		return nil
	}

	result := make([]adapter.RayReplica, 0)

	for _, applicationName := range rayserve.SortedServeApplicationNames(applications) {
		status := applications.Applications[applicationName]
		workspace, endpoint := rayserve.ApplicationIdentity(applicationName, status)

		for _, deploymentName := range rayserve.SortedDeploymentNames(status.Deployments) {
			deployment := status.Deployments[deploymentName]
			gpuQuantity, _ := rayDeploymentGPUQuantity(status, deploymentName)

			for _, replica := range deployment.Replicas {
				if replica.NodeID != nodeID || replica.ActorID == "" {
					continue
				}

				result = append(result, adapter.RayReplica{
					Workspace:   workspace,
					Endpoint:    endpoint,
					Deployment:  deploymentName,
					ActorID:     replica.ActorID,
					ReplicaID:   replica.ReplicaID,
					NodeID:      replica.NodeID,
					GPUQuantity: gpuQuantity,
				})
			}
		}
	}

	return result
}

func (p RayServeAllocationProvider) dashboardService() dashboard.DashboardService {
	if p.Dashboard != nil {
		return p.Dashboard
	}

	if strings.TrimSpace(p.DashboardURL) == "" {
		return nil
	}

	return dashboard.NewDashboardService(p.DashboardURL)
}

func (p RayServeAllocationProvider) rayNodeID(service dashboard.DashboardService) (string, error) {
	return rayserve.NodeIDByIP(service, p.NodeIP)
}

func (p RayServeAllocationProvider) processEnvReader() ProcessEnvReader {
	if p.ProcEnv != nil {
		return p.ProcEnv
	}

	return ProcFSEnvReader{}
}

func (p RayServeAllocationProvider) processDescendantReader() ProcessDescendantReader {
	if p.ProcessDescendants != nil {
		return p.ProcessDescendants
	}

	return ProcFSProcessTreeReader{Root: p.procFSRoot()}
}

// actorProcessInfo exposes only generic process identity, environment, and
// descendant topology. Adapters use it to match their own process-level
// exporter samples without making the host understand vendor process formats.
func (p RayServeAllocationProvider) actorProcessInfo(
	pid int,
	envReader ProcessEnvReader,
	descendantReader ProcessDescendantReader,
) (adapter.ProcessInfo, bool) {
	if pid <= 0 {
		return adapter.ProcessInfo{}, false
	}

	info := adapter.ProcessInfo{
		PID:            pid,
		DescendantPIDs: actorDescendantPIDs(descendantReader, pid),
	}

	if env, err := envReader.Env(pid); err == nil {
		info.Environment = env
	}

	if parentPID, ok, err := processParentPID(p.procFSRoot(), pid); err == nil && ok {
		info.ParentPID = parentPID
	}

	return info, true
}

// actorDescendantPIDs returns a stable, de-duplicated actor process tree so an
// adapter can attribute child worker processes to the owning Ray actor.
func actorDescendantPIDs(reader ProcessDescendantReader, pid int) []int {
	pids := []int{pid}
	if reader == nil {
		return pids
	}

	descendants, err := reader.DescendantPIDs(pid)
	if err != nil {
		return pids
	}

	seen := map[int]struct{}{pid: {}}

	for _, descendant := range descendants {
		if descendant <= 0 {
			continue
		}

		seen[descendant] = struct{}{}
	}

	pids = pids[:0]
	for descendant := range seen {
		pids = append(pids, descendant)
	}

	sort.Ints(pids)

	return pids
}

func (p RayServeAllocationProvider) procFSRoot() string {
	if reader, ok := p.ProcEnv.(ProcFSEnvReader); ok && strings.TrimSpace(reader.Root) != "" {
		return reader.Root
	}

	if reader, ok := p.ProcessDescendants.(ProcFSProcessTreeReader); ok && strings.TrimSpace(reader.Root) != "" {
		return reader.Root
	}

	return defaultProcFSRoot
}

type ProcessEnvReader interface {
	Env(pid int) (map[string]string, error)
}

type ProcessEnvReaderFunc func(pid int) (map[string]string, error)

func (f ProcessEnvReaderFunc) Env(pid int) (map[string]string, error) {
	return f(pid)
}

type ProcFSEnvReader struct {
	Root string
}

func (r ProcFSEnvReader) Env(pid int) (map[string]string, error) {
	root := r.Root
	if root == "" {
		root = defaultProcFSRoot
	}

	raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil, err
	}

	env := map[string]string{}

	for _, item := range strings.Split(string(raw), "\x00") {
		if item == "" {
			continue
		}

		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}

		env[key] = value
	}

	return env, nil
}

type ProcessTreeReader interface {
	IsDescendant(pid, ancestorPID int) (bool, error)
}

type ProcessTreeReaderFunc func(pid, ancestorPID int) (bool, error)

func (f ProcessTreeReaderFunc) IsDescendant(pid, ancestorPID int) (bool, error) {
	return f(pid, ancestorPID)
}

// ProcessDescendantReader observes the generic process topology rooted at an
// actor PID. It deliberately does not interpret accelerator-specific process
// metadata; adapters use the returned PIDs to join their own exporter data.
type ProcessDescendantReader interface {
	DescendantPIDs(ancestorPID int) ([]int, error)
}

type ProcessDescendantReaderFunc func(ancestorPID int) ([]int, error)

func (f ProcessDescendantReaderFunc) DescendantPIDs(ancestorPID int) ([]int, error) {
	return f(ancestorPID)
}

type ProcFSProcessTreeReader struct {
	Root string
}

func (r ProcFSProcessTreeReader) IsDescendant(pid, ancestorPID int) (bool, error) {
	if pid <= 0 || ancestorPID <= 0 {
		return false, nil
	}

	if pid == ancestorPID {
		return true, nil
	}

	root := r.Root
	if root == "" {
		root = defaultProcFSRoot
	}

	seen := map[int]struct{}{}
	currentPID := pid

	for currentPID > 1 {
		if currentPID == ancestorPID {
			return true, nil
		}

		if _, ok := seen[currentPID]; ok {
			return false, nil
		}

		seen[currentPID] = struct{}{}

		parentPID, ok, err := processParentPID(root, currentPID)
		if err != nil || !ok {
			return false, err
		}

		currentPID = parentPID
	}

	return false, nil
}

// DescendantPIDs returns the actor PID and all observable descendants beneath
// it. A process can exit while /proc is being scanned, so individual lookup
// failures are ignored and callers still receive the usable partial topology.
func (r ProcFSProcessTreeReader) DescendantPIDs(ancestorPID int) ([]int, error) {
	if ancestorPID <= 0 {
		return nil, nil
	}

	root := r.Root
	if root == "" {
		root = defaultProcFSRoot
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	pids := []int{ancestorPID}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == ancestorPID {
			continue
		}

		isDescendant, err := r.IsDescendant(pid, ancestorPID)
		if err != nil || !isDescendant {
			continue
		}

		pids = append(pids, pid)
	}

	sort.Ints(pids)

	return pids, nil
}

func processParentPID(root string, pid int) (int, bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "status"))
	if os.IsNotExist(err) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, err
	}

	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || key != "PPid" {
			continue
		}

		parentPID, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, false, err
		}

		return parentPID, true, nil
	}

	return 0, false, nil
}

func rayDeploymentGPUQuantity(status dashboard.RayServeApplicationStatus, deploymentName string) (float64, bool) {
	if status.DeployedAppConfig == nil || status.DeployedAppConfig.Args == nil || deploymentName == "" {
		return 0, false
	}

	options, ok := status.DeployedAppConfig.Args["deployment_options"].(map[string]interface{})
	if !ok {
		return 0, false
	}

	for name, raw := range options {
		if !strings.EqualFold(name, deploymentName) {
			continue
		}

		deploymentOptions, ok := raw.(map[string]interface{})
		if !ok {
			return 0, false
		}

		quantity, ok := numberAsFloat64(deploymentOptions["num_gpus"])

		return quantity, ok
	}

	return 0, false
}

func numberAsFloat64(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	default:
		return 0, false
	}
}
