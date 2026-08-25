package app

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hardware"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// nvidiaAccelerator is the Community NVIDIA adapter. It owns NVIDIA hardware
// discovery, allocation resolution, and DCGM metric conversion.
type nvidiaAccelerator struct {
	provider      hardware.GPUHardwareInfoProvider
	processReader nvidiaProcessReader
}

var (
	_ adapter.Accelerator                             = (*nvidiaAccelerator)(nil)
	_ adapter.KubernetesAccelerator                   = (*nvidiaAccelerator)(nil)
	_ adapter.StaticAccelerator                       = (*nvidiaAccelerator)(nil)
	_ adapter.StaticEvidenceEnricher                  = (*nvidiaAccelerator)(nil)
	_ adapter.EndpointReplicaAcceleratorUsageConsumer = (*nvidiaAccelerator)(nil)
	_ adapter.MetricDescriptorProvider                = (*nvidiaAccelerator)(nil)
)

// NewNVIDIAAdapter creates the Community NVIDIA adapter.
func NewNVIDIAAdapter() adapter.Accelerator {
	return &nvidiaAccelerator{}
}

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

func (a *nvidiaAccelerator) Type() string {
	return v1.AcceleratorTypeNVIDIAGPU.String()
}

// MetricDescriptors declares the NVIDIA-only metric contract. Generic
// accelerator metric families remain owned by the shared renderer.
func (*nvidiaAccelerator) MetricDescriptors() []adapter.MetricDescriptor {
	return []adapter.MetricDescriptor{{
		Name: "neutree_node_accelerator_nvidia_info",
		LabelNames: []string{
			"cluster_type",
			"node",
			"accelerator_type",
			"accelerator_uuid",
			"accelerator_index",
			"product",
			"architecture",
			"cuda_capability",
			"driver_version",
			"cuda_driver_version",
			"nvlink",
			"nvswitch",
		},
		RequiredLabelNames: []string{"accelerator_uuid"},
	}}
}

func (*nvidiaAccelerator) NeedsEndpointReplicaAcceleratorUsages() bool {
	return true
}

func (a *nvidiaAccelerator) DiscoverHardware(ctx context.Context) (adapter.HardwareSnapshot, error) {
	provider := a.provider
	if provider == nil {
		provider = hardware.NVMLGPUHardwareInfoProvider{}
	}

	infos, err := provider.GPUHardwareInfos(ctx)
	if err != nil {
		return adapter.HardwareSnapshot{
			Accelerator: v1.StaticNodeAcceleratorStatus{Type: a.Type()},
		}, nil
	}

	snapshot := adapter.HardwareSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Type:    a.Type(),
			Devices: make([]v1.StaticNodeAcceleratorDeviceStatus, 0, len(infos)),
		},
		Details: make([]adapter.HardwareDetails, 0, len(infos)),
	}

	for _, info := range infos {
		device := v1.StaticNodeAcceleratorDeviceStatus{
			ID:           info.Index,
			UUID:         info.UUID,
			ProductName:  info.Product,
			ProductModel: info.Product,
			Healthy:      true,
		}

		if info.MemoryTotalMiB != "" {
			var memoryMiB int64
			if _, scanErr := fmt.Sscan(info.MemoryTotalMiB, &memoryMiB); scanErr == nil {
				device.MemoryMiB = memoryMiB
			}
		}

		if info.MinorNumber != "" {
			var minor int
			if _, scanErr := fmt.Sscan(info.MinorNumber, &minor); scanErr == nil {
				device.MinorNumber = &minor
			}
		}

		snapshot.Accelerator.Devices = append(snapshot.Accelerator.Devices, device)
		snapshot.Details = append(snapshot.Details, adapter.HardwareDetails{
			UUID:           info.UUID,
			Architecture:   info.Architecture,
			DriverVersion:  info.DriverVersion,
			PCIEBusID:      info.PCIEBusID,
			PCIEGeneration: info.PCIEGeneration,
			PCIEWidth:      info.PCIEWidth,
			NUMANode:       info.NUMANode,
		})
	}

	return snapshot, nil
}

func (a *nvidiaAccelerator) BuildKubernetesMetrics(
	ctx context.Context,
	hardwareSnapshot adapter.HardwareSnapshot,
	evidence adapter.KubernetesEvidence,
) (adapter.MetricResult, error) {
	allocations, err := nvidiaKubernetesAllocations(hardwareSnapshot, evidence)
	if err != nil {
		return adapter.MetricResult{}, err
	}
	endpointReplicaGPUUsages := internalEndpointReplicaAcceleratorUsages(
		evidence.Common.EndpointReplicaAcceleratorUsages,
	)
	result := a.buildMetrics(
		ctx,
		hardwareSnapshot,
		evidence.Common,
		nvidiaEndpointAllocations(evidence.Common.Labels, allocations),
		endpointReplicaGPUUsages,
	)
	result.Allocations = allocations

	return result, nil
}

func (a *nvidiaAccelerator) BuildStaticMetrics(
	ctx context.Context,
	hardwareSnapshot adapter.HardwareSnapshot,
	evidence adapter.StaticEvidence,
) (adapter.MetricResult, error) {
	allocations := nvidiaStaticAllocations(hardwareSnapshot, evidence)
	endpointReplicaGPUUsages := internalEndpointReplicaAcceleratorUsages(
		evidence.Common.EndpointReplicaAcceleratorUsages,
	)
	result := a.buildMetrics(
		ctx,
		hardwareSnapshot,
		evidence.Common,
		nvidiaEndpointAllocations(evidence.Common.Labels, allocations),
		endpointReplicaGPUUsages,
	)
	result.Allocations = allocations

	return result, nil
}

func (a *nvidiaAccelerator) buildMetrics(
	_ context.Context,
	hardwareSnapshot adapter.HardwareSnapshot,
	evidence adapter.CommonEvidence,
	endpointAllocations []model.EndpointAllocation,
	endpointReplicaGPUUsages []model.EndpointReplicaGPUUsage,
) adapter.MetricResult {
	labels := internalLabels(evidence.Labels)
	if !evidence.ExporterUp {
		return adapter.MetricResult{
			Samples: adapterSamplesFromNormalizer(normalizer.NormalizeEndpointReplicaGPUUsageSamples(
				labels,
				endpointReplicaGPUUsages,
				endpointAllocations,
				nil,
			)),
		}
	}

	raw := evidence.ExporterText
	hardwareInfos := gpuHardwareInfosFromSnapshot(hardwareSnapshot)
	acceleratorIndexes := normalizer.AcceleratorIndexesByUUID(raw, hardwareInfos)

	samples := make([]normalizer.Sample, 0, len(endpointAllocations)*2+8)
	samples = append(samples, normalizer.NormalizeAcceleratorSamples(labels, raw)...)
	samples = append(samples, normalizer.NormalizeNodeGPUSamples(labels, raw, endpointAllocations)...)
	samples = append(samples, normalizer.NormalizeGPUHardwareInfoSamples(labels, hardwareInfos, raw)...)
	samples = append(samples, normalizer.NormalizeEndpointAllocationSamples(
		labels,
		endpointAllocations,
		endpointReplicaGPUUsages,
		acceleratorIndexes,
		raw,
	)...)
	samples = append(samples, normalizer.NormalizeEndpointReplicaGPUUsageFromDCGMSamples(
		labels,
		raw,
		endpointAllocations,
		endpointReplicaGPUUsages,
	)...)
	samples = append(samples, normalizer.NormalizeEndpointReplicaGPUUsageSamples(
		labels,
		endpointReplicaGPUUsages,
		endpointAllocations,
		acceleratorIndexes,
	)...)

	return adapter.MetricResult{Samples: adapterSamplesFromNormalizer(samples)}
}

func gpuHardwareInfosFromSnapshot(hardwareSnapshot adapter.HardwareSnapshot) []model.GPUHardwareInfo {
	if len(hardwareSnapshot.Accelerator.Devices) == 0 {
		return nil
	}

	detailsByUUID := make(map[string]adapter.HardwareDetails, len(hardwareSnapshot.Details))

	for _, detail := range hardwareSnapshot.Details {
		if detail.UUID != "" {
			detailsByUUID[detail.UUID] = detail
		}
	}

	result := make([]model.GPUHardwareInfo, 0, len(hardwareSnapshot.Accelerator.Devices))

	for _, device := range hardwareSnapshot.Accelerator.Devices {
		detail := detailsByUUID[device.UUID]
		result = append(result, model.GPUHardwareInfo{
			UUID:           device.UUID,
			Index:          device.ID,
			Product:        model.FirstNonEmpty(device.ProductModel, device.ProductName),
			Architecture:   detail.Architecture,
			DriverVersion:  detail.DriverVersion,
			MemoryTotalMiB: fmt.Sprintf("%d", device.MemoryMiB),
			PCIEBusID:      detail.PCIEBusID,
			PCIEGeneration: detail.PCIEGeneration,
			PCIEWidth:      detail.PCIEWidth,
			NUMANode:       detail.NUMANode,
		})
	}

	return result
}
