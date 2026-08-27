package allocation

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/ray/rayserve"
)

const (
	endpointWorkloadType = "endpoint"
	defaultProcFSRoot    = "/proc"
)

type Provider interface {
	Allocations(ctx context.Context, snapshot *v1.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error)
}

type ProviderFunc func(ctx context.Context, snapshot *v1.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error)

func (f ProviderFunc) Allocations(
	ctx context.Context,
	snapshot *v1.NodeDeviceSnapshot,
) ([]v1.StaticNodeAllocationStatus, error) {
	return f(ctx, snapshot)
}

type MultiProvider struct {
	Providers []Provider
}

func (p MultiProvider) Allocations(
	ctx context.Context,
	snapshot *v1.NodeDeviceSnapshot,
) ([]v1.StaticNodeAllocationStatus, error) {
	allocations := make([]v1.StaticNodeAllocationStatus, 0)

	for _, provider := range p.Providers {
		if provider == nil {
			continue
		}

		providerAllocations, err := provider.Allocations(ctx, snapshot)
		if err != nil {
			return nil, err
		}

		allocations = append(allocations, providerAllocations...)
	}

	return allocations, nil
}

type PodResourceLister interface {
	ListPodResources(ctx context.Context) ([]adapter.PodResource, error)
}

type PodResourceListerFunc func(ctx context.Context) ([]adapter.PodResource, error)

func (f PodResourceListerFunc) ListPodResources(ctx context.Context) ([]adapter.PodResource, error) {
	return f(ctx)
}

func firstNonEmpty(values ...string) string {
	return model.FirstNonEmpty(values...)
}

type KubernetesAllocationProvider struct {
	Client       client.Client
	NodeName     string
	PodResources PodResourceLister
}

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

func (p KubernetesAllocationProvider) Allocations(
	ctx context.Context,
	snapshot *v1.NodeDeviceSnapshot,
) ([]v1.StaticNodeAllocationStatus, error) {
	if p.Client == nil || p.NodeName == "" || p.PodResources == nil || snapshot == nil {
		return nil, nil
	}

	podResources, err := p.PodResources.ListPodResources(ctx)
	if err != nil {
		return nil, err
	}

	deviceLookup := newDeviceLookup(snapshot.Accelerator.Devices)
	allocations := make([]v1.StaticNodeAllocationStatus, 0, len(podResources))

	for _, podResource := range podResources {
		allocation, ok, err := p.podAllocation(ctx, podResource, deviceLookup)
		if err != nil {
			return nil, err
		}

		if ok {
			allocations = append(allocations, allocation)
		}
	}

	sortStaticNodeAllocations(allocations)

	return allocations, nil
}

func (p KubernetesAllocationProvider) podAllocation(
	ctx context.Context,
	podResource adapter.PodResource,
	deviceLookup acceleratorDeviceLookup,
) (v1.StaticNodeAllocationStatus, bool, error) {
	devices := allocationDevicesFromRefs(
		containerDeviceRefs(podResource.Containers),
		deviceLookup,
		p.NodeName,
	)
	if len(devices) == 0 {
		return v1.StaticNodeAllocationStatus{}, false, nil
	}

	pod := &corev1.Pod{}
	if err := p.Client.Get(ctx, client.ObjectKey{Namespace: podResource.Namespace, Name: podResource.Name}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return v1.StaticNodeAllocationStatus{}, false, nil
		}

		return v1.StaticNodeAllocationStatus{}, false, err
	}

	if pod.Spec.NodeName != p.NodeName {
		return v1.StaticNodeAllocationStatus{}, false, nil
	}

	labels := pod.GetLabels()
	allocation := v1.StaticNodeAllocationStatus{
		WorkloadType: endpointWorkloadType,
		Workspace:    labels[v1.NeutreeClusterWorkspaceLabelKey],
		Endpoint:     labels["endpoint"],
		InstanceID:   podResource.Name,
		ReplicaID:    podResource.Name,
		RuntimeID:    podResource.Namespace + "/" + podResource.Name,
		Devices:      devices,
	}

	return allocation, true, nil
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
	Node               string
	NodeIP             string
	ProcEnv            ProcessEnvReader
	GPUProcesses       GPUProcessReader
	ProcessTree        ProcessTreeReader
	ProcessDescendants ProcessDescendantReader
}

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

	var replicas []adapter.RayReplica
	if applicationsErr == nil {
		replicas = rayReplicasFromApplications(applications, nodeID)
	}

	return adapter.StaticEvidence{
		// A missing Serve-applications response cannot prove that there are no
		// endpoint replicas. Preserve independent actor evidence, but keep
		// allocation-derived metrics absent for this collection cycle.
		AllocationAvailable: applicationsErr == nil && applications != nil,
		RayEvidence: adapter.RayEvidence{
			Actors:         rayActorsFromDashboard(actors),
			Replicas:       replicas,
			ActorProcesses: actorProcesses,
		},
	}, nil
}

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

func (p RayServeAllocationProvider) Allocations(
	ctx context.Context,
	snapshot *v1.NodeDeviceSnapshot,
) ([]v1.StaticNodeAllocationStatus, error) {
	if snapshot == nil {
		return nil, nil
	}

	service := p.dashboardService()
	if service == nil || p.NodeIP == "" {
		return nil, nil
	}

	nodeID, err := p.rayNodeID(service)
	if err != nil || nodeID == "" {
		return nil, err
	}

	applications, err := service.GetServeApplications()
	if err != nil {
		return nil, err
	}

	deviceLookup := newDeviceLookup(snapshot.Accelerator.Devices)
	envReader := p.processEnvReader()

	gpuProcesses, err := p.gpuProcessReader().GPUProcesses(ctx)
	if err != nil {
		return nil, err
	}

	processTree := p.processTreeReader()
	nodeLabel := firstNonEmpty(p.Node, p.NodeIP, nodeID)
	allocations := make([]v1.StaticNodeAllocationStatus, 0)

	for _, appName := range rayserve.SortedServeApplicationNames(applications) {
		status := applications.Applications[appName]
		for _, deploymentName := range rayserve.SortedDeploymentNames(status.Deployments) {
			deployment := status.Deployments[deploymentName]
			for _, replica := range deployment.Replicas {
				if replica.NodeID != nodeID || replica.ActorID == "" {
					continue
				}

				allocation, ok, err := rayReplicaAllocation(
					service,
					envReader,
					appName,
					status,
					deploymentName,
					replica,
					deviceLookup,
					nodeLabel,
					gpuProcesses,
					processTree,
				)
				if err != nil {
					return nil, err
				}

				if ok {
					allocations = append(allocations, allocation)
				}
			}
		}
	}

	sortStaticNodeAllocations(allocations)

	return allocations, nil
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

func (p RayServeAllocationProvider) gpuProcessReader() GPUProcessReader {
	if p.GPUProcesses != nil {
		return p.GPUProcesses
	}

	return NvidiaSMIGPUProcessReader{}
}

func (p RayServeAllocationProvider) processTreeReader() ProcessTreeReader {
	if p.ProcessTree != nil {
		return p.ProcessTree
	}

	return ProcFSProcessTreeReader{}
}

func (p RayServeAllocationProvider) processDescendantReader() ProcessDescendantReader {
	if p.ProcessDescendants != nil {
		return p.ProcessDescendants
	}

	return ProcFSProcessTreeReader{Root: p.procFSRoot()}
}

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

	if reader, ok := p.ProcessTree.(ProcFSProcessTreeReader); ok && strings.TrimSpace(reader.Root) != "" {
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

type GPUProcessReader interface {
	GPUProcesses(ctx context.Context) ([]GPUProcess, error)
}

type GPUProcessReaderFunc func(ctx context.Context) ([]GPUProcess, error)

func (f GPUProcessReaderFunc) GPUProcesses(ctx context.Context) ([]GPUProcess, error) {
	return f(ctx)
}

type GPUProcess struct {
	UUID          string
	PID           int
	UsedMemoryMiB int64
}

type NvidiaSMIGPUProcessReader struct {
	Command string
}

func (r NvidiaSMIGPUProcessReader) GPUProcesses(ctx context.Context) ([]GPUProcess, error) {
	command := r.Command
	if command == "" {
		command = "nvidia-smi"
	}

	out, err := exec.CommandContext(
		ctx,
		command,
		"--query-compute-apps=gpu_uuid,pid,used_memory",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		return nil, nil
	}

	return parseNvidiaSMIComputeProcesses(string(out)), nil
}

func parseNvidiaSMIComputeProcesses(raw string) []GPUProcess {
	processes := make([]GPUProcess, 0)

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}

		uuid := strings.TrimSpace(parts[0])
		if uuid == "" {
			continue
		}

		pid, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || pid <= 0 {
			continue
		}

		process := GPUProcess{UUID: uuid, PID: pid}

		if len(parts) >= 3 {
			if usedMemoryMiB, ok := parseFirstInt64(parts[2]); ok {
				process.UsedMemoryMiB = usedMemoryMiB
			}
		}

		processes = append(processes, process)
	}

	return processes
}

func parseFirstInt64(value string) (int64, bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return 0, false
	}

	parsed, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, false
	}

	return parsed, true
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

type acceleratorDeviceLookup struct {
	byUUID map[string]v1.StaticNodeAcceleratorDeviceStatus
	byID   map[string]v1.StaticNodeAcceleratorDeviceStatus
	all    []v1.StaticNodeAcceleratorDeviceStatus
}

func newDeviceLookup(devices []v1.StaticNodeAcceleratorDeviceStatus) acceleratorDeviceLookup {
	lookup := acceleratorDeviceLookup{
		byUUID: map[string]v1.StaticNodeAcceleratorDeviceStatus{},
		byID:   map[string]v1.StaticNodeAcceleratorDeviceStatus{},
		all:    append([]v1.StaticNodeAcceleratorDeviceStatus{}, devices...),
	}

	for _, device := range devices {
		if device.UUID != "" {
			lookup.byUUID[device.UUID] = device
		}

		if device.ID != "" {
			lookup.byID[device.ID] = device
		}
	}

	return lookup
}

func containerDeviceRefs(containers []adapter.ContainerDevices) []string {
	refs := make([]string, 0)
	for _, container := range containers {
		refs = append(refs, container.DeviceIDs...)
	}

	return refs
}

func rayReplicaAllocation(
	service dashboard.DashboardService,
	envReader ProcessEnvReader,
	appName string,
	status dashboard.RayServeApplicationStatus,
	deploymentName string,
	replica dashboard.Replica,
	deviceLookup acceleratorDeviceLookup,
	nodeLabel string,
	gpuProcesses []GPUProcess,
	processTree ProcessTreeReader,
) (v1.StaticNodeAllocationStatus, bool, error) {
	actor, err := rayserve.ActorByID(service, replica.ActorID)
	if err != nil {
		return v1.StaticNodeAllocationStatus{}, false, err
	}

	if actor == nil || actor.PID <= 0 {
		return v1.StaticNodeAllocationStatus{}, false, nil
	}

	env, err := envReader.Env(actor.PID)
	if err != nil {
		return v1.StaticNodeAllocationStatus{}, false, err
	}

	gpuQuantity, hasGPUQuantity := rayDeploymentGPUQuantity(status, deploymentName)
	if hasGPUQuantity && gpuQuantity <= 0 {
		return v1.StaticNodeAllocationStatus{}, false, nil
	}

	devices := allocationDevicesFromRefsWithQuantity(visibleDeviceRefs(env, deviceLookup), deviceLookup, nodeLabel, gpuQuantity)

	if processTree != nil {
		processDevices, err := allocationDevicesFromGPUProcesses(
			gpuProcesses,
			processTree,
			actor.PID,
			deviceLookup,
			nodeLabel,
			gpuQuantity,
		)
		if err != nil {
			return v1.StaticNodeAllocationStatus{}, false, err
		}

		if len(processDevices) > 0 {
			devices = mergeAllocationDeviceUsage(devices, processDevices)
		}
	}

	if len(devices) == 0 {
		return v1.StaticNodeAllocationStatus{}, false, nil
	}

	workspace, endpoint := rayserve.ApplicationIdentity(appName, status)
	replicaID := firstNonEmpty(replica.ReplicaID, replica.ActorID)

	return v1.StaticNodeAllocationStatus{
		WorkloadType: endpointWorkloadType,
		Workspace:    workspace,
		Endpoint:     endpoint,
		InstanceID:   replica.ActorID,
		ReplicaID:    replicaID,
		RuntimeID:    replica.ActorID,
		PID:          actor.PID,
		Devices:      devices,
	}, true, nil
}

func mergeAllocationDeviceUsage(
	allocatedDevices []v1.DeviceAllocation,
	processDevices []v1.DeviceAllocation,
) []v1.DeviceAllocation {
	if len(allocatedDevices) == 0 {
		return processDevices
	}

	usedMemoryMiBByUUID := make(map[string]int64, len(processDevices))

	for _, device := range processDevices {
		if device.UUID == "" {
			continue
		}

		usedMemoryMiBByUUID[device.UUID] += device.UsedMemoryMiB
	}

	for i := range allocatedDevices {
		if allocatedDevices[i].UUID == "" {
			continue
		}

		allocatedDevices[i].UsedMemoryMiB = usedMemoryMiBByUUID[allocatedDevices[i].UUID]
	}

	return allocatedDevices
}

func visibleDeviceRefs(env map[string]string, deviceLookup acceleratorDeviceLookup) []string {
	nvidiaVisibleDevices := strings.TrimSpace(env["NVIDIA_VISIBLE_DEVICES"])
	if hasExactVisibleDeviceUUIDs(nvidiaVisibleDevices, deviceLookup) {
		return parseVisibleDevices(nvidiaVisibleDevices)
	}

	if value := strings.TrimSpace(env["CUDA_VISIBLE_DEVICES"]); value != "" {
		return parseVisibleDevices(value)
	}

	return parseVisibleDevices(nvidiaVisibleDevices)
}

func hasExactVisibleDeviceUUIDs(value string, deviceLookup acceleratorDeviceLookup) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}

	switch strings.ToLower(value) {
	case "all", "none", "void", "no":
		return false
	}

	for _, ref := range strings.Split(value, ",") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}

		if _, ok := deviceLookup.byUUID[ref]; !ok {
			return false
		}
	}

	return true
}

func parseVisibleDevices(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	switch strings.ToLower(value) {
	case "all", "none", "void", "no":
		return nil
	}

	parts := strings.Split(value, ",")
	refs := make([]string, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			refs = append(refs, part)
		}
	}

	return refs
}

func allocationDevicesFromRefs(
	refs []string,
	deviceLookup acceleratorDeviceLookup,
	nodeID string,
) []v1.DeviceAllocation {
	return allocationDevicesFromRefsWithUsageAndQuantity(refs, deviceLookup, nodeID, nil, 0)
}

func allocationDevicesFromRefsWithQuantity(
	refs []string,
	deviceLookup acceleratorDeviceLookup,
	nodeID string,
	gpuQuantity float64,
) []v1.DeviceAllocation {
	return allocationDevicesFromRefsWithUsageAndQuantity(refs, deviceLookup, nodeID, nil, gpuQuantity)
}

func allocationDevicesFromRefsWithUsageAndQuantity(
	refs []string,
	deviceLookup acceleratorDeviceLookup,
	nodeID string,
	usedMemoryMiBByUUID map[string]int64,
	gpuQuantity float64,
) []v1.DeviceAllocation {
	devices := make([]v1.DeviceAllocation, 0, len(refs))
	seen := map[string]struct{}{}

	for _, ref := range refs {
		device, ok := deviceFromRef(ref, deviceLookup)
		if !ok || device.UUID == "" {
			continue
		}

		if _, exists := seen[device.UUID]; exists {
			continue
		}

		seen[device.UUID] = struct{}{}
		memoryMiB, coreUnits := allocationDeviceCapacity(device, gpuQuantity)

		allocation := v1.DeviceAllocation{
			UUID:      device.UUID,
			Product:   firstNonEmpty(device.ProductModel, device.ProductName),
			MemoryMiB: memoryMiB,
			CoreUnits: coreUnits,
			NodeID:    nodeID,
		}
		if usedMemoryMiBByUUID != nil {
			allocation.UsedMemoryMiB = usedMemoryMiBByUUID[device.UUID]
		}

		devices = append(devices, allocation)
	}

	return devices
}

func allocationDevicesFromGPUProcesses(
	gpuProcesses []GPUProcess,
	processTree ProcessTreeReader,
	actorPID int,
	deviceLookup acceleratorDeviceLookup,
	nodeID string,
	gpuQuantity float64,
) ([]v1.DeviceAllocation, error) {
	refs := make([]string, 0, len(gpuProcesses))
	usedMemoryMiBByUUID := map[string]int64{}

	for _, gpuProcess := range gpuProcesses {
		descendant, err := processTree.IsDescendant(gpuProcess.PID, actorPID)
		if err != nil {
			return nil, err
		}

		if descendant {
			refs = append(refs, gpuProcess.UUID)
			usedMemoryMiBByUUID[gpuProcess.UUID] += gpuProcess.UsedMemoryMiB
		}
	}

	return allocationDevicesFromRefsWithUsageAndQuantity(refs, deviceLookup, nodeID, usedMemoryMiBByUUID, gpuQuantity), nil
}

func allocationDeviceCapacity(device v1.StaticNodeAcceleratorDeviceStatus, gpuQuantity float64) (int64, int64) {
	if gpuQuantity > 0 && gpuQuantity < 1 {
		return int64(math.Round(float64(device.MemoryMiB) * gpuQuantity)), int64(math.Round(100 * gpuQuantity))
	}

	return device.MemoryMiB, 100
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

func deviceFromRef(
	ref string,
	deviceLookup acceleratorDeviceLookup,
) (v1.StaticNodeAcceleratorDeviceStatus, bool) {
	if ref == "" {
		return v1.StaticNodeAcceleratorDeviceStatus{}, false
	}

	if device, ok := deviceLookup.byUUID[ref]; ok {
		return device, true
	}

	if device, ok := deviceLookup.byID[ref]; ok {
		return device, true
	}

	return v1.StaticNodeAcceleratorDeviceStatus{}, false
}

func sortStaticNodeAllocations(allocations []v1.StaticNodeAllocationStatus) {
	sort.SliceStable(allocations, func(i, j int) bool {
		if allocations[i].Workspace != allocations[j].Workspace {
			return allocations[i].Workspace < allocations[j].Workspace
		}

		if allocations[i].Endpoint != allocations[j].Endpoint {
			return allocations[i].Endpoint < allocations[j].Endpoint
		}

		if allocations[i].InstanceID != allocations[j].InstanceID {
			return allocations[i].InstanceID < allocations[j].InstanceID
		}

		return allocations[i].RuntimeID < allocations[j].RuntimeID
	})
}

func EndpointAllocationsFromStaticNodeAllocations(
	labels model.CanonicalLabels,
	allocations []v1.StaticNodeAllocationStatus,
) []model.EndpointAllocation {
	result := make([]model.EndpointAllocation, 0, len(allocations))

	for _, allocation := range allocations {
		if allocation.WorkloadType != "" && allocation.WorkloadType != endpointWorkloadType {
			continue
		}

		if allocation.Endpoint == "" || len(allocation.Devices) == 0 {
			continue
		}

		result = append(result, model.EndpointAllocation{
			Workspace:  firstNonEmpty(allocation.Workspace, labels.Workspace),
			Cluster:    labels.NeutreeCluster,
			Endpoint:   allocation.Endpoint,
			InstanceID: allocation.InstanceID,
			ReplicaID:  allocation.ReplicaID,
			NodeID:     firstNonEmpty(labels.Node, labels.NodeIP),
			Devices:    append([]v1.DeviceAllocation{}, allocation.Devices...),
		})
	}

	return result
}
