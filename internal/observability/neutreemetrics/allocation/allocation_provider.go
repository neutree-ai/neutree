package allocation

import (
	"context"
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
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/ray/rayserve"
)

const endpointWorkloadType = "endpoint"

type Provider interface {
	Allocations(ctx context.Context, snapshot *model.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error)
}

type ProviderFunc func(ctx context.Context, snapshot *model.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error)

func (f ProviderFunc) Allocations(
	ctx context.Context,
	snapshot *model.NodeDeviceSnapshot,
) ([]v1.StaticNodeAllocationStatus, error) {
	return f(ctx, snapshot)
}

type MultiProvider struct {
	Providers []Provider
}

func (p MultiProvider) Allocations(
	ctx context.Context,
	snapshot *model.NodeDeviceSnapshot,
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
	ListPodResources(ctx context.Context) ([]model.PodResource, error)
}

type PodResourceListerFunc func(ctx context.Context) ([]model.PodResource, error)

func (f PodResourceListerFunc) ListPodResources(ctx context.Context) ([]model.PodResource, error) {
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

func (p KubernetesAllocationProvider) Allocations(
	ctx context.Context,
	snapshot *model.NodeDeviceSnapshot,
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
	podResource model.PodResource,
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

type RayServeAllocationProvider struct {
	Dashboard    dashboard.DashboardService
	DashboardURL string
	Node         string
	NodeIP       string
	ProcEnv      ProcessEnvReader
	GPUProcesses GPUProcessReader
	ProcessTree  ProcessTreeReader
}

func (p RayServeAllocationProvider) Allocations(
	ctx context.Context,
	snapshot *model.NodeDeviceSnapshot,
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
					ctx,
					service,
					envReader,
					appName,
					status,
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
		root = "/proc"
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
		root = "/proc"
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

func containerDeviceRefs(containers []model.ContainerDevices) []string {
	refs := make([]string, 0)
	for _, container := range containers {
		refs = append(refs, container.DeviceIDs...)
	}

	return refs
}

func rayReplicaAllocation(
	ctx context.Context,
	service dashboard.DashboardService,
	envReader ProcessEnvReader,
	appName string,
	status dashboard.RayServeApplicationStatus,
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

	devices := allocationDevicesFromRefs(visibleDeviceRefs(env, deviceLookup), deviceLookup, nodeLabel)

	if processTree != nil {
		processDevices, err := allocationDevicesFromGPUProcesses(
			gpuProcesses,
			processTree,
			actor.PID,
			deviceLookup,
			nodeLabel,
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

func rayActorByID(
	_ context.Context,
	service dashboard.DashboardService,
	actorID string,
) (*dashboard.Actor, error) {
	actors, err := service.ListActors(
		[]dashboard.ActorFilter{{Key: "actor_id", Predicate: "=", Value: actorID}},
		true,
		1,
	)
	if err != nil {
		return nil, err
	}

	if actors == nil || len(actors.Data.Result.Result) == 0 {
		return nil, nil
	}

	return &actors.Data.Result.Result[0], nil
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
	return allocationDevicesFromRefsWithUsage(refs, deviceLookup, nodeID, nil)
}

func allocationDevicesFromRefsWithUsage(
	refs []string,
	deviceLookup acceleratorDeviceLookup,
	nodeID string,
	usedMemoryMiBByUUID map[string]int64,
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

		allocation := v1.DeviceAllocation{
			UUID:      device.UUID,
			Product:   firstNonEmpty(device.ProductModel, device.ProductName),
			MemoryMiB: device.MemoryMiB,
			CoreUnits: 100,
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

	return allocationDevicesFromRefsWithUsage(refs, deviceLookup, nodeID, usedMemoryMiBByUUID), nil
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

func sortedServeApplicationNames(resp *dashboard.RayServeApplicationsResponse) []string {
	if resp == nil {
		return nil
	}

	names := make([]string, 0, len(resp.Applications))
	for name := range resp.Applications {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func sortedDeploymentNames(deployments map[string]dashboard.Deployment) []string {
	names := make([]string, 0, len(deployments))
	for name := range deployments {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func splitServeApplicationName(appName string) (string, string) {
	workspace, endpoint, ok := strings.Cut(appName, "_")
	if !ok {
		return "", appName
	}

	return workspace, endpoint
}

func serveApplicationIdentity(
	appName string,
	status dashboard.RayServeApplicationStatus,
) (string, string) {
	if status.DeployedAppConfig != nil {
		workspace, endpoint, ok := parseServeRoutePrefix(status.DeployedAppConfig.RoutePrefix)
		if ok {
			return workspace, endpoint
		}
	}

	return splitServeApplicationName(appName)
}

func parseServeRoutePrefix(routePrefix string) (string, string, bool) {
	routePrefix = strings.Trim(routePrefix, "/")
	if routePrefix == "" {
		return "", "", false
	}

	workspace, endpoint, ok := strings.Cut(routePrefix, "/")
	if !ok || workspace == "" || endpoint == "" || strings.Contains(endpoint, "/") {
		return "", "", false
	}

	return workspace, endpoint, true
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
