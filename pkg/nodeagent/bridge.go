package nodeagent

import (
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func internalLabels(labels adapter.CanonicalLabels) model.CanonicalLabels {
	return model.CanonicalLabels{
		Workspace:         labels.Workspace,
		NeutreeCluster:    labels.NeutreeCluster,
		StaticNodeCluster: labels.StaticNodeCluster,
		ClusterType:       labels.ClusterType,
		Node:              labels.Node,
		NodeIP:            labels.NodeIP,
		NodeRole:          labels.NodeRole,
	}
}

func adapterSamplesFromNormalizer(samples []normalizer.Sample) []adapter.Sample {
	result := make([]adapter.Sample, 0, len(samples))
	for _, sample := range samples {
		labels := make(map[string]string, len(sample.Labels))
		for key, value := range sample.Labels {
			labels[key] = value
		}
		result = append(result, adapter.Sample{Name: sample.Name, Labels: labels, Value: sample.Value})
	}

	return result
}

func internalEndpointReplicaAcceleratorUsages(
	usages []adapter.EndpointReplicaAcceleratorUsage,
) []model.EndpointReplicaGPUUsage {
	result := make([]model.EndpointReplicaGPUUsage, 0, len(usages))
	for _, usage := range usages {
		converted := model.EndpointReplicaGPUUsage{
			Workspace:        usage.Workspace,
			Cluster:          usage.Cluster,
			Endpoint:         usage.Endpoint,
			InstanceID:       usage.InstanceID,
			ReplicaID:        usage.ReplicaID,
			NodeID:           usage.NodeID,
			Container:        usage.Container,
			GPUUUID:          usage.AcceleratorUUID,
			AcceleratorType:  usage.AcceleratorType,
			AcceleratorIndex: usage.AcceleratorIndex,
			VDeviceIndex:     usage.VDeviceIndex,
			Product:          usage.Product,
		}
		if usage.MemoryUsedBytes != nil {
			memoryUsedBytes := *usage.MemoryUsedBytes
			converted.MemoryUsedBytes = &memoryUsedBytes
		}
		if usage.UtilizationRatio != nil {
			utilizationRatio := *usage.UtilizationRatio
			converted.UtilizationRatio = &utilizationRatio
		}
		result = append(result, converted)
	}

	return result
}
