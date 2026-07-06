package hami

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
)

const (
	endpointWorkloadType       = "endpoint"
	hamiDevicePluginComponent  = "hami-device-plugin"
	hamiMonitorPort            = 9394
	defaultHAMiHTTPTimeout     = 5 * time.Second
	hamiAllocationFieldCount   = 4
	hamiMemoryBytesPerMiB      = 1024 * 1024
	hamiNodeNvidiaRegister     = "hami.io/node-nvidia-register"
	hamiVGPUDevicesAllocated   = "hami.io/vgpu-devices-allocated"
	hamiMetricMemoryLimitBytes = "hami_vgpu_memory_limit_bytes"
	hamiMetricMemoryUsedBytes  = "hami_vgpu_memory_used_bytes"
	hamiMetricUtilizationRatio = "hami_container_device_utilization_ratio"
)

type KubernetesProvider struct {
	Client     client.Client
	NodeName   string
	HTTPClient *http.Client
}

type podKey struct {
	namespace string
	name      string
}

type podIdentity struct {
	workspace string
	cluster   string
	endpoint  string
	node      string
}

type gpuUsageKey struct {
	namespace    string
	pod          string
	container    string
	deviceUUID   string
	vdeviceIndex string
	node         string
}

func (p KubernetesProvider) Usages(ctx context.Context) ([]model.EndpointReplicaGPUUsage, error) {
	if p.Client == nil || p.NodeName == "" {
		return nil, nil
	}

	pods, err := p.localEndpointPods(ctx)
	if err != nil {
		return nil, err
	}

	if len(pods) == 0 {
		return nil, nil
	}

	monitorPod, ok, err := p.localMonitorPod(ctx)
	if err != nil || !ok {
		return nil, err
	}

	raw, err := p.scrapeMonitor(ctx, monitorPod.Status.PodIP)
	if err != nil {
		return nil, err
	}

	return endpointGPUUsagesFromHAMiMetrics(raw, podIdentities(pods)), nil
}

func (p KubernetesProvider) Allocations(
	ctx context.Context,
	_ *v1.NodeDeviceSnapshot,
) ([]v1.StaticNodeAllocationStatus, error) {
	if p.Client == nil || p.NodeName == "" {
		return nil, nil
	}

	pods, err := p.localEndpointPods(ctx)
	if err != nil {
		return nil, err
	}

	products, err := p.deviceProducts(ctx)
	if err != nil {
		return nil, err
	}

	allocations := make([]v1.StaticNodeAllocationStatus, 0, len(pods))

	for _, pod := range pods {
		devices, err := hamiDeviceAllocationsFromAnnotation(
			pod.Annotations[hamiVGPUDevicesAllocated],
			pod.Spec.NodeName,
			products,
		)
		if err != nil {
			return nil, fmt.Errorf("parse HAMi allocation for pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}

		if len(devices) == 0 {
			continue
		}

		labels := pod.GetLabels()
		allocations = append(allocations, v1.StaticNodeAllocationStatus{
			WorkloadType: endpointWorkloadType,
			Workspace:    endpointWorkspace(labels),
			Endpoint:     labels["endpoint"],
			InstanceID:   pod.Name,
			ReplicaID:    pod.Name,
			RuntimeID:    pod.Namespace + "/" + pod.Name,
			Devices:      devices,
		})
	}

	sortStaticNodeAllocations(allocations)

	return allocations, nil
}

func (p KubernetesProvider) localEndpointPods(ctx context.Context) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := p.Client.List(
		ctx,
		podList,
		client.MatchingFields{"spec.nodeName": p.NodeName},
		client.MatchingLabels{"app": "inference"},
	); err != nil {
		return nil, fmt.Errorf("list pods for HAMi endpoint usage: %w", err)
	}

	pods := make([]corev1.Pod, 0)

	for _, pod := range podList.Items {
		if pod.Spec.NodeName != p.NodeName || terminalPodPhase(pod.Status.Phase) {
			continue
		}

		labels := pod.GetLabels()
		if labels["app"] != "inference" || labels["endpoint"] == "" {
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

func (p KubernetesProvider) localMonitorPod(ctx context.Context) (corev1.Pod, bool, error) {
	podList := &corev1.PodList{}
	if err := p.Client.List(
		ctx,
		podList,
		client.MatchingFields{"spec.nodeName": p.NodeName},
		client.MatchingLabels{"app.kubernetes.io/component": hamiDevicePluginComponent},
	); err != nil {
		return corev1.Pod{}, false, fmt.Errorf("list pods for HAMi monitor: %w", err)
	}

	for _, pod := range podList.Items {
		if pod.Spec.NodeName != p.NodeName || terminalPodPhase(pod.Status.Phase) || pod.Status.PodIP == "" {
			continue
		}

		if pod.Labels["app.kubernetes.io/component"] != hamiDevicePluginComponent {
			continue
		}

		return pod, true, nil
	}

	return corev1.Pod{}, false, nil
}

func (p KubernetesProvider) scrapeMonitor(ctx context.Context, podIP string) (string, error) {
	if strings.TrimSpace(podIP) == "" {
		return "", nil
	}

	url := fmt.Sprintf("http://%s:%d/metrics", podIP, hamiMonitorPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)

	if err != nil {
		return "", err
	}

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("HAMi monitor returned status %d", resp.StatusCode)
	}

	return string(body), nil
}

func (p KubernetesProvider) httpClient() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}

	return &http.Client{Timeout: defaultHAMiHTTPTimeout}
}

func (p KubernetesProvider) deviceProducts(ctx context.Context) (map[string]string, error) {
	node := &corev1.Node{}
	if err := p.Client.Get(ctx, client.ObjectKey{Name: p.NodeName}, node); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("get local node %s for HAMi device products: %w", p.NodeName, err)
	}

	return deviceProductsFromHAMiNodeAnnotation(node.Annotations[hamiNodeNvidiaRegister])
}

func podIdentities(pods []corev1.Pod) map[podKey]podIdentity {
	identities := make(map[podKey]podIdentity, len(pods))

	for _, pod := range pods {
		labels := pod.GetLabels()
		identities[podKey{namespace: pod.Namespace, name: pod.Name}] = podIdentity{
			workspace: endpointWorkspace(labels),
			cluster:   labels[v1.NeutreeClusterLabelKey],
			endpoint:  labels["endpoint"],
			node:      pod.Spec.NodeName,
		}
	}

	return identities
}

func endpointGPUUsagesFromHAMiMetrics(
	raw string,
	pods map[podKey]podIdentity,
) []model.EndpointReplicaGPUUsage {
	index := map[gpuUsageKey]*model.EndpointReplicaGPUUsage{}

	for _, sample := range promtext.ParseVector(raw) {
		key := gpuUsageKey{
			namespace:    promtext.LabelValue(sample, "namespace"),
			pod:          promtext.LabelValue(sample, "pod"),
			container:    promtext.LabelValue(sample, "container"),
			deviceUUID:   promtext.LabelValue(sample, "device_uuid", "gpu_uuid", "uuid"),
			vdeviceIndex: promtext.LabelValue(sample, "vdevice_index"),
			node:         promtext.LabelValue(sample, "node"),
		}
		if key.namespace == "" || key.pod == "" || key.deviceUUID == "" {
			continue
		}

		identity, ok := pods[podKey{namespace: key.namespace, name: key.pod}]
		if !ok || identity.endpoint == "" {
			continue
		}

		usage := index[key]
		if usage == nil {
			usage = &model.EndpointReplicaGPUUsage{
				Workspace:       identity.workspace,
				Cluster:         identity.cluster,
				Endpoint:        identity.endpoint,
				InstanceID:      key.pod,
				ReplicaID:       key.pod,
				NodeID:          firstNonEmpty(key.node, identity.node),
				Container:       key.container,
				GPUUUID:         key.deviceUUID,
				AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
				VDeviceIndex:    key.vdeviceIndex,
				Product: firstNonEmpty(
					promtext.LabelValue(sample, "device_name"),
					promtext.LabelValue(sample, "product"),
					promtext.LabelValue(sample, "modelName"),
					promtext.LabelValue(sample, "model"),
				),
			}
			index[key] = usage
		}

		switch promtext.MetricName(sample) {
		case hamiMetricMemoryLimitBytes:
			usage.MemoryAllocatedBytes = addFloat64Pointer(usage.MemoryAllocatedBytes, promtext.Value(sample))
		case hamiMetricMemoryUsedBytes:
			usage.MemoryUsedBytes = addFloat64Pointer(usage.MemoryUsedBytes, promtext.Value(sample))
		case hamiMetricUtilizationRatio:
			value := normalizedRatio(promtext.Value(sample))
			usage.UtilizationRatio = maxFloat64Pointer(usage.UtilizationRatio, value)
		}
	}

	result := make([]model.EndpointReplicaGPUUsage, 0, len(index))
	for _, usage := range index {
		result = append(result, *usage)
	}

	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Endpoint != result[j].Endpoint {
			return result[i].Endpoint < result[j].Endpoint
		}

		if result[i].InstanceID != result[j].InstanceID {
			return result[i].InstanceID < result[j].InstanceID
		}

		if result[i].Container != result[j].Container {
			return result[i].Container < result[j].Container
		}

		return result[i].GPUUUID < result[j].GPUUUID
	})

	return result
}

func hamiDeviceAllocationsFromAnnotation(
	value string,
	nodeName string,
	products map[string]string,
) ([]v1.DeviceAllocation, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	devices := make([]v1.DeviceAllocation, 0)

	for _, entry := range strings.Split(value, ";") {
		for _, segment := range strings.Split(entry, ":") {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				continue
			}

			fields := strings.Split(segment, ",")
			if len(fields) < hamiAllocationFieldCount {
				continue
			}

			uuid := strings.TrimSpace(fields[0])
			memoryMiB, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)

			if err != nil {
				return nil, fmt.Errorf("invalid memory value %q: %w", fields[2], err)
			}

			coreUnits, err := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid core value %q: %w", fields[3], err)
			}

			devices = append(devices, v1.DeviceAllocation{
				UUID:      uuid,
				Product:   firstNonEmpty(products[uuid], strings.TrimSpace(fields[1])),
				NodeID:    nodeName,
				MemoryMiB: memoryMiB,
				CoreUnits: coreUnits,
			})
		}
	}

	sort.SliceStable(devices, func(i, j int) bool {
		return devices[i].UUID < devices[j].UUID
	})

	return devices, nil
}

type hamiRegisteredDevice struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

func deviceProductsFromHAMiNodeAnnotation(value string) (map[string]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	var devices []hamiRegisteredDevice
	if err := json.Unmarshal([]byte(value), &devices); err != nil {
		return nil, fmt.Errorf("parse %s: %w", hamiNodeNvidiaRegister, err)
	}

	products := make(map[string]string, len(devices))

	for _, device := range devices {
		if device.ID == "" || device.Type == "" {
			continue
		}

		products[device.ID] = device.Type
	}

	return products, nil
}

func sortStaticNodeAllocations(allocations []v1.StaticNodeAllocationStatus) {
	sort.SliceStable(allocations, func(i, j int) bool {
		if allocations[i].Endpoint != allocations[j].Endpoint {
			return allocations[i].Endpoint < allocations[j].Endpoint
		}

		if allocations[i].InstanceID != allocations[j].InstanceID {
			return allocations[i].InstanceID < allocations[j].InstanceID
		}

		return allocations[i].RuntimeID < allocations[j].RuntimeID
	})
}

func endpointWorkspace(labels map[string]string) string {
	return firstNonEmpty(labels["workspace"], labels[v1.NeutreeClusterWorkspaceLabelKey])
}

func terminalPodPhase(phase corev1.PodPhase) bool {
	return phase == corev1.PodFailed || phase == corev1.PodSucceeded
}

func addFloat64Pointer(current *float64, value float64) *float64 {
	if current != nil {
		value += *current
	}

	return float64Pointer(value)
}

func maxFloat64Pointer(current *float64, value float64) *float64 {
	if current != nil && *current > value {
		return current
	}

	return float64Pointer(value)
}

func float64Pointer(value float64) *float64 {
	return &value
}

func normalizedRatio(value float64) float64 {
	if value > 1 {
		return value / 100
	}

	return value
}

func firstNonEmpty(values ...string) string {
	return model.FirstNonEmpty(values...)
}
