package app

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
	metricskubernetes "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

const (
	hamiDevicePluginComponent  = "hami-device-plugin"
	hamiMonitorPort            = 9394
	defaultHAMiHTTPTimeout     = 5 * time.Second
	hamiMetricMemoryUsedBytes  = "hami_vgpu_memory_used_bytes"
	hamiMetricUtilizationRatio = "hami_container_device_utilization_ratio"
)

// nvidiaHAMiKubernetesUsageProvider collects NVIDIA virtual-device usage from
// the local HAMi device-plugin monitor. It is private to the NVIDIA adapter.
type nvidiaHAMiKubernetesUsageProvider struct {
	Client     client.Client
	NodeName   string
	HTTPClient *http.Client
}

// configureNVIDIAKubernetesUsage binds optional HAMi collection to the
// NVIDIA adapter during application assembly. The generic metrics host only
// sees the adapter's evidence-enrichment capability.
func configureNVIDIAKubernetesUsage(
	registry adapterRegistry,
	writer *metricskubernetes.AnnotationWriter,
) {
	if writer == nil {
		return
	}

	accelerator, ok := registry.byType[v1.AcceleratorTypeNVIDIAGPU.String()].(*nvidiaAccelerator)
	if !ok {
		return
	}

	accelerator.endpointUsageProvider = nvidiaHAMiKubernetesUsageProvider{
		Client:   writer.Client,
		NodeName: writer.NodeName,
	}
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

func (p nvidiaHAMiKubernetesUsageProvider) Usages(ctx context.Context) ([]adapter.EndpointReplicaAcceleratorUsage, error) {
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

	return nvidiaHAMiEndpointReplicaUsagesFromMetrics(raw, podIdentities(pods)), nil
}

func (p nvidiaHAMiKubernetesUsageProvider) localEndpointPods(ctx context.Context) ([]corev1.Pod, error) {
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

func (p nvidiaHAMiKubernetesUsageProvider) localMonitorPod(ctx context.Context) (corev1.Pod, bool, error) {
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

func (p nvidiaHAMiKubernetesUsageProvider) scrapeMonitor(ctx context.Context, podIP string) (string, error) {
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

func (p nvidiaHAMiKubernetesUsageProvider) httpClient() *http.Client {
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
			workspace: nvidiaHAMiEndpointWorkspace(labels),
			cluster:   labels[v1.NeutreeClusterLabelKey],
			endpoint:  labels["endpoint"],
			node:      pod.Spec.NodeName,
		}
	}

	return identities
}

func nvidiaHAMiEndpointReplicaUsagesFromMetrics(
	raw string,
	pods map[podKey]podIdentity,
) []adapter.EndpointReplicaAcceleratorUsage {
	index := map[gpuUsageKey]*adapter.EndpointReplicaAcceleratorUsage{}

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
			usage = &adapter.EndpointReplicaAcceleratorUsage{
				Workspace:       identity.workspace,
				Cluster:         identity.cluster,
				Endpoint:        identity.endpoint,
				InstanceID:      key.pod,
				ReplicaID:       key.pod,
				NodeID:          firstNonEmpty(key.node, identity.node),
				Container:       key.container,
				AcceleratorUUID: key.deviceUUID,
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

	result := make([]adapter.EndpointReplicaAcceleratorUsage, 0, len(index))
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

		return result[i].AcceleratorUUID < result[j].AcceleratorUUID
	})

	return result
}

func nvidiaHAMiEndpointWorkspace(labels map[string]string) string {
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
