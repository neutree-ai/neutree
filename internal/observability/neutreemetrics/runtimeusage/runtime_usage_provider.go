package runtimeusage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/ray/rayserve"
	prommodel "github.com/prometheus/common/model"
)

const (
	defaultProcFSRoot         = "/proc"
	defaultCGroupFSRoot       = "/sys/fs/cgroup"
	kubernetesPodApp          = "inference"
	rayServeBackendDeployment = "Backend"
)

type Provider interface {
	Usages(ctx context.Context) ([]model.EndpointReplicaRuntimeUsage, error)
}

type ProviderFunc func(ctx context.Context) ([]model.EndpointReplicaRuntimeUsage, error)

func (f ProviderFunc) Usages(ctx context.Context) ([]model.EndpointReplicaRuntimeUsage, error) {
	return f(ctx)
}

func firstNonEmpty(values ...string) string {
	return model.FirstNonEmpty(values...)
}

type ContainerRuntimeUsage struct {
	ContainerID           string
	CPUUsageSeconds       float64
	MemoryUsageBytes      *float64
	MemoryWorkingSetBytes *float64
	CPULimitCores         *float64
	MemoryLimitBytes      *float64
}

type CGroupUsageReader interface {
	UsageForPID(pid int) (ContainerRuntimeUsage, bool, error)
}

type CGroupUsageReaderFunc func(pid int) (ContainerRuntimeUsage, bool, error)

func (f CGroupUsageReaderFunc) UsageForPID(pid int) (ContainerRuntimeUsage, bool, error) {
	return f(pid)
}

type CGroupFSUsageReader struct {
	ProcFSRoot   string
	CGroupFSRoot string
}

func (r CGroupFSUsageReader) UsageForPID(pid int) (ContainerRuntimeUsage, bool, error) {
	procRoot := firstNonEmpty(r.ProcFSRoot, defaultProcFSRoot)
	cgroupRoot := firstNonEmpty(r.CGroupFSRoot, defaultCGroupFSRoot)
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return ContainerRuntimeUsage{}, false, err
	}

	paths := parseProcessCGroupPaths(string(raw))
	if paths.cpu != "" || paths.memory != "" {
		return r.readCGroupV1(cgroupRoot, paths)
	}

	if paths.unified != "" {
		return r.readCGroupV2(cgroupRoot, paths.unified)
	}

	return ContainerRuntimeUsage{}, false, nil
}

type processCGroupPaths struct {
	unified string
	cpu     string
	memory  string
}

func parseProcessCGroupPaths(raw string) processCGroupPaths {
	var paths processCGroupPaths

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}

		if parts[0] == "0" && parts[1] == "" {
			paths.unified = parts[2]
			continue
		}

		controllers := strings.Split(parts[1], ",")
		for _, controller := range controllers {
			switch controller {
			case "cpu", "cpuacct":
				paths.cpu = parts[2]
			case "memory":
				paths.memory = parts[2]
			}
		}
	}

	return paths
}

func (r CGroupFSUsageReader) readCGroupV2(root string, cgroupPath string) (ContainerRuntimeUsage, bool, error) {
	dir := cgroupPathJoin(root, cgroupPath)
	cpuUsage, ok, err := readKeyedFloat(filepath.Join(dir, "cpu.stat"), "usage_usec")
	if err != nil || !ok {
		return ContainerRuntimeUsage{}, false, err
	}

	memoryUsage, hasMemoryUsage, err := readSingleFloat(filepath.Join(dir, "memory.current"))
	if err != nil {
		return ContainerRuntimeUsage{}, false, err
	}

	usage := ContainerRuntimeUsage{
		ContainerID:     containerIDFromCGroupPath(cgroupPath),
		CPUUsageSeconds: cpuUsage / 1_000_000,
	}

	if hasMemoryUsage {
		usage.MemoryUsageBytes = float64Ptr(memoryUsage)
		usage.MemoryWorkingSetBytes = float64Ptr(memoryUsage)
		if inactive, ok, err := readKeyedFloat(filepath.Join(dir, "memory.stat"), "inactive_file"); err != nil {
			return ContainerRuntimeUsage{}, false, err
		} else if ok {
			usage.MemoryWorkingSetBytes = float64Ptr(mathMax(memoryUsage-inactive, 0))
		}
	}

	if limit, ok, err := readCGroupV2CPULimit(filepath.Join(dir, "cpu.max")); err != nil {
		return ContainerRuntimeUsage{}, false, err
	} else if ok {
		usage.CPULimitCores = float64Ptr(limit)
	}

	if limit, ok, err := readCGroupV2MemoryLimit(filepath.Join(dir, "memory.max")); err != nil {
		return ContainerRuntimeUsage{}, false, err
	} else if ok {
		usage.MemoryLimitBytes = float64Ptr(limit)
	}

	return usage, true, nil
}

func (r CGroupFSUsageReader) readCGroupV1(
	root string,
	paths processCGroupPaths,
) (ContainerRuntimeUsage, bool, error) {
	cpuDir := cgroupPathJoin(filepath.Join(root, "cpu,cpuacct"), paths.cpu)
	if _, err := os.Stat(cpuDir); err != nil {
		cpuDir = cgroupPathJoin(filepath.Join(root, "cpuacct"), paths.cpu)
	}

	cpuUsage, ok, err := readSingleFloat(filepath.Join(cpuDir, "cpuacct.usage"))
	if err != nil || !ok {
		return ContainerRuntimeUsage{}, false, err
	}

	usage := ContainerRuntimeUsage{
		ContainerID:     containerIDFromCGroupPath(firstNonEmpty(paths.cpu, paths.memory)),
		CPUUsageSeconds: cpuUsage / 1_000_000_000,
	}

	if paths.memory != "" {
		memoryDir := cgroupPathJoin(filepath.Join(root, "memory"), paths.memory)
		memoryUsage, hasMemoryUsage, err := readSingleFloat(filepath.Join(memoryDir, "memory.usage_in_bytes"))
		if err != nil {
			return ContainerRuntimeUsage{}, false, err
		}
		if hasMemoryUsage {
			usage.MemoryUsageBytes = float64Ptr(memoryUsage)
			usage.MemoryWorkingSetBytes = float64Ptr(memoryUsage)
			if inactive, ok, err := readKeyedFloat(
				filepath.Join(memoryDir, "memory.stat"),
				"total_inactive_file",
			); err != nil {
				return ContainerRuntimeUsage{}, false, err
			} else if ok {
				usage.MemoryWorkingSetBytes = float64Ptr(mathMax(memoryUsage-inactive, 0))
			}
		}

		if limit, ok, err := readSingleFloat(filepath.Join(memoryDir, "memory.limit_in_bytes")); err != nil {
			return ContainerRuntimeUsage{}, false, err
		} else if ok {
			usage.MemoryLimitBytes = float64Ptr(limit)
		}
	}

	if limit, ok, err := readCGroupV1CPULimit(cpuDir); err != nil {
		return ContainerRuntimeUsage{}, false, err
	} else if ok {
		usage.CPULimitCores = float64Ptr(limit)
	}

	return usage, true, nil
}

type RayServeRuntimeUsageProvider struct {
	Dashboard    dashboard.DashboardService
	DashboardURL string
	Node         string
	NodeIP       string
	CGroupUsage  CGroupUsageReader
}

func (p RayServeRuntimeUsageProvider) Usages(ctx context.Context) ([]model.EndpointReplicaRuntimeUsage, error) {
	service := p.dashboardService()
	if service == nil || p.NodeIP == "" {
		return nil, nil
	}

	nodeID, err := rayserve.NodeIDByIP(service, p.NodeIP)
	if err != nil || nodeID == "" {
		return nil, err
	}

	applications, err := service.GetServeApplications()
	if err != nil {
		return nil, err
	}

	cgroupUsage := p.cgroupUsageReader()
	nodeLabel := firstNonEmpty(p.Node, p.NodeIP, nodeID)
	usages := make([]model.EndpointReplicaRuntimeUsage, 0)

	for _, appName := range rayserve.SortedServeApplicationNames(applications) {
		status := applications.Applications[appName]
		for _, deploymentName := range rayserve.SortedDeploymentNames(status.Deployments) {
			if deploymentName != rayServeBackendDeployment {
				continue
			}

			deployment := status.Deployments[deploymentName]
			for _, replica := range deployment.Replicas {
				if replica.NodeID != nodeID || replica.ActorID == "" {
					continue
				}

				usage, ok, err := rayReplicaRuntimeUsage(
					service,
					cgroupUsage,
					appName,
					status,
					deploymentName,
					replica,
					nodeLabel,
				)
				if err != nil {
					return nil, err
				}
				if ok {
					usages = append(usages, usage)
				}
			}
		}
	}

	sortEndpointReplicaRuntimeUsages(usages)

	return usages, nil
}

func (p RayServeRuntimeUsageProvider) dashboardService() dashboard.DashboardService {
	if p.Dashboard != nil {
		return p.Dashboard
	}

	if strings.TrimSpace(p.DashboardURL) == "" {
		return nil
	}

	return dashboard.NewDashboardService(p.DashboardURL)
}

func (p RayServeRuntimeUsageProvider) cgroupUsageReader() CGroupUsageReader {
	if p.CGroupUsage != nil {
		return p.CGroupUsage
	}

	return CGroupFSUsageReader{}
}

func rayReplicaRuntimeUsage(
	service dashboard.DashboardService,
	reader CGroupUsageReader,
	appName string,
	status dashboard.RayServeApplicationStatus,
	deploymentName string,
	replica dashboard.Replica,
	nodeLabel string,
) (model.EndpointReplicaRuntimeUsage, bool, error) {
	actor, err := rayserve.ActorByID(service, replica.ActorID)
	if err != nil {
		return model.EndpointReplicaRuntimeUsage{}, false, err
	}

	if actor == nil || actor.PID <= 0 {
		return model.EndpointReplicaRuntimeUsage{}, false, nil
	}

	containerUsage, ok, err := reader.UsageForPID(actor.PID)
	if err != nil || !ok {
		return model.EndpointReplicaRuntimeUsage{}, false, err
	}

	workspace, endpoint := rayserve.ApplicationIdentity(appName, status)
	replicaID := firstNonEmpty(replica.ReplicaID, replica.ActorID)

	return model.EndpointReplicaRuntimeUsage{
		Workspace:             workspace,
		Cluster:               "",
		Endpoint:              endpoint,
		InstanceID:            replica.ActorID,
		ReplicaID:             replicaID,
		NodeID:                nodeLabel,
		WorkloadRole:          model.WorkloadRoleBackend,
		Container:             deploymentName,
		ContainerID:           containerUsage.ContainerID,
		CPUUsageSeconds:       containerUsage.CPUUsageSeconds,
		MemoryUsageBytes:      containerUsage.MemoryUsageBytes,
		MemoryWorkingSetBytes: containerUsage.MemoryWorkingSetBytes,
		CPULimitCores:         containerUsage.CPULimitCores,
		MemoryLimitBytes:      containerUsage.MemoryLimitBytes,
	}, true, nil
}

type CAdvisorScraper interface {
	Scrape(ctx context.Context) (string, error)
}

type CAdvisorScraperFunc func(ctx context.Context) (string, error)

func (f CAdvisorScraperFunc) Scrape(ctx context.Context) (string, error) {
	return f(ctx)
}

type KubernetesCAdvisorRuntimeUsageProvider struct {
	Client   client.Client
	NodeName string
	Scraper  CAdvisorScraper
}

func (p KubernetesCAdvisorRuntimeUsageProvider) Usages(ctx context.Context) ([]model.EndpointReplicaRuntimeUsage, error) {
	if p.Client == nil || p.NodeName == "" || p.Scraper == nil {
		return nil, nil
	}

	pods, err := p.endpointPodsByMetricKey(ctx)
	if err != nil {
		return nil, err
	}
	if len(pods) == 0 {
		return nil, nil
	}

	raw, err := p.Scraper.Scrape(ctx)
	if err != nil {
		return nil, err
	}

	usages := endpointReplicaRuntimeUsagesFromCAdvisor(raw, pods)
	sortEndpointReplicaRuntimeUsages(usages)

	return usages, nil
}

func (p KubernetesCAdvisorRuntimeUsageProvider) endpointPodsByMetricKey(
	ctx context.Context,
) (map[podContainerMetricKey]kubernetesEndpointContainer, error) {
	podList := &corev1.PodList{}
	if err := p.Client.List(
		ctx,
		podList,
		client.MatchingFields{"spec.nodeName": p.NodeName},
		client.MatchingLabels{"app": kubernetesPodApp},
	); err != nil {
		return nil, err
	}

	result := map[podContainerMetricKey]kubernetesEndpointContainer{}
	for i := range podList.Items {
		pod := podList.Items[i]
		if pod.Spec.NodeName != p.NodeName || !isRunningEndpointPod(pod) {
			continue
		}

		statuses := containerStatusesByName(pod.Status.ContainerStatuses)
		for _, container := range pod.Spec.Containers {
			status := statuses[container.Name]
			result[podContainerMetricKey{
				namespace: pod.Namespace,
				pod:       pod.Name,
				container: container.Name,
			}] = kubernetesEndpointContainer{
				pod:       pod,
				container: container,
				status:    status,
			}
		}
	}

	return result, nil
}

type podContainerMetricKey struct {
	namespace string
	pod       string
	container string
}

type kubernetesEndpointContainer struct {
	pod       corev1.Pod
	container corev1.Container
	status    corev1.ContainerStatus
}

func endpointReplicaRuntimeUsagesFromCAdvisor(
	raw string,
	containers map[podContainerMetricKey]kubernetesEndpointContainer,
) []model.EndpointReplicaRuntimeUsage {
	index := map[podContainerMetricKey]*model.EndpointReplicaRuntimeUsage{}

	for _, metric := range promtext.ParseVector(raw) {
		key, ok := cAdvisorPodContainerMetricKey(metric)
		if !ok {
			continue
		}

		container, ok := containers[key]
		if !ok {
			continue
		}

		usage := index[key]
		if usage == nil {
			next := runtimeUsageFromKubernetesContainer(container)
			usage = &next
			index[key] = usage
		}

		switch promtext.MetricName(metric) {
		case "container_cpu_usage_seconds_total":
			usage.CPUUsageSeconds = promtext.Value(metric)
		case "container_memory_usage_bytes":
			usage.MemoryUsageBytes = float64Ptr(promtext.Value(metric))
		case "container_memory_working_set_bytes":
			usage.MemoryWorkingSetBytes = float64Ptr(promtext.Value(metric))
		}
	}

	result := make([]model.EndpointReplicaRuntimeUsage, 0, len(index))
	for _, usage := range index {
		result = append(result, *usage)
	}

	return result
}

func runtimeUsageFromKubernetesContainer(container kubernetesEndpointContainer) model.EndpointReplicaRuntimeUsage {
	labels := container.pod.Labels
	cpuLimit := cpuLimitCores(container.container.Resources.Limits)
	memoryLimit := memoryLimitBytes(container.container.Resources.Limits)

	return model.EndpointReplicaRuntimeUsage{
		Workspace:        labels["workspace"],
		Cluster:          labels["cluster"],
		Endpoint:         labels["endpoint"],
		InstanceID:       container.pod.Name,
		ReplicaID:        container.pod.Name,
		NodeID:           container.pod.Spec.NodeName,
		WorkloadRole:     model.WorkloadRoleBackend,
		Container:        container.container.Name,
		ContainerID:      normalizeContainerID(container.status.ContainerID),
		Engine:           labels["engine"],
		EngineVersion:    labels["engine_version"],
		CPULimitCores:    cpuLimit,
		MemoryLimitBytes: memoryLimit,
	}
}

func cAdvisorPodContainerMetricKey(metric *prommodel.Sample) (podContainerMetricKey, bool) {
	container := promtext.LabelValue(metric, "container", "container_name")
	if container == "" || container == "POD" {
		return podContainerMetricKey{}, false
	}

	namespace := promtext.LabelValue(metric, "namespace", "pod_namespace")
	pod := promtext.LabelValue(metric, "pod", "pod_name")
	if namespace == "" || pod == "" {
		return podContainerMetricKey{}, false
	}

	return podContainerMetricKey{namespace: namespace, pod: pod, container: container}, true
}

func isRunningEndpointPod(pod corev1.Pod) bool {
	return pod.Status.Phase != corev1.PodFailed &&
		pod.Status.Phase != corev1.PodSucceeded &&
		pod.Labels["app"] == kubernetesPodApp &&
		pod.Labels["endpoint"] != ""
}

func containerStatusesByName(statuses []corev1.ContainerStatus) map[string]corev1.ContainerStatus {
	result := make(map[string]corev1.ContainerStatus, len(statuses))
	for _, status := range statuses {
		result[status.Name] = status
	}

	return result
}

func readKeyedFloat(path string, key string) (float64, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}

		return 0, false, err
	}

	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != key {
			continue
		}

		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return 0, false, err
		}

		return value, true, nil
	}

	return 0, false, nil
}

func readSingleFloat(path string) (float64, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}

		return 0, false, err
	}

	value := strings.TrimSpace(string(raw))
	if value == "" || value == "max" {
		return 0, false, nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false, err
	}

	return parsed, true, nil
}

func readCGroupV2CPULimit(path string) (float64, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}

		return 0, false, err
	}

	fields := strings.Fields(string(raw))
	if len(fields) < 2 || fields[0] == "max" {
		return 0, false, nil
	}

	quota, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false, err
	}
	period, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, false, err
	}
	if quota <= 0 || period <= 0 {
		return 0, false, nil
	}

	return quota / period, true, nil
}

func readCGroupV2MemoryLimit(path string) (float64, bool, error) {
	return readSingleFloat(path)
}

func readCGroupV1CPULimit(cpuDir string) (float64, bool, error) {
	quota, hasQuota, err := readSingleFloat(filepath.Join(cpuDir, "cpu.cfs_quota_us"))
	if err != nil || !hasQuota || quota <= 0 {
		return 0, false, err
	}

	period, hasPeriod, err := readSingleFloat(filepath.Join(cpuDir, "cpu.cfs_period_us"))
	if err != nil || !hasPeriod || period <= 0 {
		return 0, false, err
	}

	return quota / period, true, nil
}

func cgroupPathJoin(root string, cgroupPath string) string {
	return filepath.Join(root, strings.TrimPrefix(cgroupPath, "/"))
}

func containerIDFromCGroupPath(cgroupPath string) string {
	base := filepath.Base(cgroupPath)
	base = strings.TrimSuffix(base, ".scope")
	base = strings.TrimPrefix(base, "docker-")
	base = strings.TrimPrefix(base, "cri-containerd-")
	base = strings.TrimPrefix(base, "crio-")

	return base
}

func normalizeContainerID(containerID string) string {
	if _, value, ok := strings.Cut(containerID, "://"); ok {
		return value
	}

	return containerID
}

func cpuLimitCores(resources corev1.ResourceList) *float64 {
	return quantityAsFloat64(resources, corev1.ResourceCPU, func(q resource.Quantity) float64 {
		return float64(q.MilliValue()) / 1000
	})
}

func memoryLimitBytes(resources corev1.ResourceList) *float64 {
	return quantityAsFloat64(resources, corev1.ResourceMemory, func(q resource.Quantity) float64 {
		return float64(q.Value())
	})
}

func quantityAsFloat64(
	resources corev1.ResourceList,
	name corev1.ResourceName,
	valueFn func(resource.Quantity) float64,
) *float64 {
	quantity, ok := resources[name]
	if !ok {
		return nil
	}

	value := valueFn(quantity)

	return &value
}

func float64Ptr(value float64) *float64 {
	return &value
}

func mathMax(a, b float64) float64 {
	if a > b {
		return a
	}

	return b
}

func sortEndpointReplicaRuntimeUsages(usages []model.EndpointReplicaRuntimeUsage) {
	sort.SliceStable(usages, func(i, j int) bool {
		if usages[i].Workspace != usages[j].Workspace {
			return usages[i].Workspace < usages[j].Workspace
		}
		if usages[i].Endpoint != usages[j].Endpoint {
			return usages[i].Endpoint < usages[j].Endpoint
		}
		if usages[i].ReplicaID != usages[j].ReplicaID {
			return usages[i].ReplicaID < usages[j].ReplicaID
		}
		if usages[i].Container != usages[j].Container {
			return usages[i].Container < usages[j].Container
		}

		return usages[i].ContainerID < usages[j].ContainerID
	})
}

type KubernetesNodeProxyCAdvisorScraper struct {
	RESTClient rest.Interface
	NodeName   string
}

func (s KubernetesNodeProxyCAdvisorScraper) Scrape(ctx context.Context) (string, error) {
	if s.RESTClient == nil || s.NodeName == "" {
		return "", fmt.Errorf("kubernetes cAdvisor scraper requires REST client and node name")
	}

	raw, err := s.RESTClient.Get().
		Resource("nodes").
		Name(s.NodeName).
		SubResource("proxy").
		Suffix("metrics", "cadvisor").
		DoRaw(ctx)
	if err != nil {
		return "", err
	}

	return string(raw), nil
}
