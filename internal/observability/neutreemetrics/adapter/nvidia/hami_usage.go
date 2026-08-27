package nvidia

import (
	"sort"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

const (
	hamiMetricMemoryUsedBytes  = "hami_vgpu_memory_used_bytes"
	hamiMetricUtilizationRatio = "hami_container_device_utilization_ratio"
)

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

// nvidiaHAMiEndpointReplicaUsagesFromEvidence decodes the generic host's raw
// virtualization-monitor exposition using NVIDIA HAMi labels and semantics.
// Collection stays outside the adapter so the same profile can select the
// appropriate monitor for any accelerator type.
func nvidiaHAMiEndpointReplicaUsagesFromEvidence(
	evidence adapter.KubernetesEvidence,
) []model.EndpointReplicaGPUUsage {
	if !evidence.VirtualizationMonitor.Up || strings.TrimSpace(evidence.VirtualizationMonitor.Text) == "" {
		return nil
	}

	return nvidiaHAMiEndpointReplicaUsagesFromMetrics(
		evidence.VirtualizationMonitor.Text,
		podIdentities(evidence.EndpointPods),
	)
}

func podIdentities(pods []adapter.EndpointPodEvidence) map[podKey]podIdentity {
	identities := make(map[podKey]podIdentity, len(pods))

	for _, pod := range pods {
		identities[podKey{namespace: pod.Namespace, name: pod.Name}] = podIdentity{
			workspace: nvidiaHAMiEndpointWorkspace(pod.Labels),
			cluster:   pod.Labels[v1.NeutreeClusterLabelKey],
			endpoint:  pod.Labels["endpoint"],
			node:      pod.NodeName,
		}
	}

	return identities
}

func nvidiaHAMiEndpointReplicaUsagesFromMetrics(
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
			usage.UtilizationRatio = maxFloat64Pointer(usage.UtilizationRatio, normalizedRatio(promtext.Value(sample)))
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

func nvidiaHAMiEndpointWorkspace(labels map[string]string) string {
	return firstNonEmpty(labels["workspace"], labels[v1.NeutreeClusterWorkspaceLabelKey])
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
