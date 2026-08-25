package hami

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	hamiMemoryBytesPerMiB      = 1024 * 1024
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
