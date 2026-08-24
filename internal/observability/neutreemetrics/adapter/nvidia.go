package adapter

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/devicesnapshot"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hardware"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
)

func init() { //nolint:gochecknoinits
	Register(&nvidiaAccelerator{})
}

// nvidiaAccelerator is the reference implementation of the Accelerator
// interface. It reuses the normalizer's legacy DCGM conversion functions so the
// adapter path produces output byte-identical to the legacy normalizer path
// (existing DCGM assertions stay green), while validating the adapter
// framework end to end.
type nvidiaAccelerator struct {
	provider hardware.GPUHardwareInfoProvider
}

var (
	_ Accelerator           = (*nvidiaAccelerator)(nil)
	_ KubernetesAccelerator = (*nvidiaAccelerator)(nil)
	_ StaticAccelerator     = (*nvidiaAccelerator)(nil)
)

func (a *nvidiaAccelerator) Type() string {
	return v1.AcceleratorTypeNVIDIAGPU.String()
}

func (a *nvidiaAccelerator) DiscoverHardware(ctx context.Context) (model.AcceleratorHardwareSnapshot, error) {
	provider := a.provider
	if provider == nil {
		provider = hardware.NVMLGPUHardwareInfoProvider{}
	}

	infos, err := provider.GPUHardwareInfos(ctx)
	if err != nil {
		return model.AcceleratorHardwareSnapshot{
			Accelerator: v1.StaticNodeAcceleratorStatus{Type: a.Type()},
		}, nil
	}

	snapshot := model.AcceleratorHardwareSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Type:    a.Type(),
			Devices: make([]v1.StaticNodeAcceleratorDeviceStatus, 0, len(infos)),
		},
		Details: make([]model.AcceleratorHardwareDetails, 0, len(infos)),
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
		snapshot.Details = append(snapshot.Details, model.AcceleratorHardwareDetails{
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
	hardware model.AcceleratorHardwareSnapshot,
	evidence KubernetesAcceleratorEvidence,
) (AcceleratorMetricResult, error) {
	return a.buildMetrics(ctx, hardware, evidence.Common, nil, nil)
}

func (a *nvidiaAccelerator) BuildStaticMetrics(
	ctx context.Context,
	hardware model.AcceleratorHardwareSnapshot,
	evidence StaticAcceleratorEvidence,
) (AcceleratorMetricResult, error) {
	return a.buildMetrics(ctx, hardware, evidence.Common, nil, nil)
}

func (a *nvidiaAccelerator) BuildMetrics(
	ctx context.Context,
	evidence AcceleratorEvidence,
) (AcceleratorMetricResult, error) {
	if evidence.AcceleratorType != "" && evidence.AcceleratorType != a.Type() {
		return AcceleratorMetricResult{}, fmt.Errorf(
			"adapter %q selected for accelerator type %q",
			a.Type(),
			evidence.AcceleratorType,
		)
	}

	return a.buildMetrics(
		ctx,
		hardwareSnapshotFromLegacyEvidence(evidence),
		CommonAcceleratorEvidence{
			ExporterText: evidence.ExporterText,
			ExporterUp:   evidence.ExporterUp,
			Labels:       evidence.Labels,
		},
		evidence.EndpointAllocations,
		evidence.EndpointReplicaGPUUsages,
	)
}

func (a *nvidiaAccelerator) buildMetrics(
	_ context.Context,
	hardware model.AcceleratorHardwareSnapshot,
	evidence CommonAcceleratorEvidence,
	endpointAllocations []model.EndpointAllocation,
	endpointReplicaGPUUsages []model.EndpointReplicaGPUUsage,
) (AcceleratorMetricResult, error) {
	if !evidence.ExporterUp {
		return AcceleratorMetricResult{
			Samples: normalizer.NormalizeEndpointReplicaGPUUsageSamples(
				evidence.Labels,
				endpointReplicaGPUUsages,
				endpointAllocations,
				nil,
			),
		}, nil
	}

	labels := evidence.Labels
	raw := evidence.ExporterText
	hardwareInfos := gpuHardwareInfosFromSnapshot(hardware)
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

	return AcceleratorMetricResult{Samples: samples}, nil
}

type AcceleratorEvidence struct {
	AcceleratorType          string
	ExporterText             string
	ExporterUp               bool
	Labels                   model.CanonicalLabels
	EndpointAllocations      []model.EndpointAllocation
	GPUHardwareInfos         []model.GPUHardwareInfo
	EndpointReplicaGPUUsages []model.EndpointReplicaGPUUsage
}

func hardwareSnapshotFromLegacyEvidence(evidence AcceleratorEvidence) model.AcceleratorHardwareSnapshot {
	snapshot := model.AcceleratorHardwareSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{
			Type:    v1.AcceleratorTypeNVIDIAGPU.String(),
			Devices: make([]v1.StaticNodeAcceleratorDeviceStatus, 0, len(evidence.GPUHardwareInfos)),
		},
		Details: make([]model.AcceleratorHardwareDetails, 0, len(evidence.GPUHardwareInfos)),
	}

	for _, info := range evidence.GPUHardwareInfos {
		device := v1.StaticNodeAcceleratorDeviceStatus{
			ID:           info.Index,
			UUID:         info.UUID,
			ProductName:  info.Product,
			ProductModel: info.Product,
			Healthy:      true,
		}
		snapshot.Accelerator.Devices = append(snapshot.Accelerator.Devices, device)
		snapshot.Details = append(snapshot.Details, model.AcceleratorHardwareDetails{
			UUID:           info.UUID,
			Architecture:   info.Architecture,
			DriverVersion:  info.DriverVersion,
			PCIEBusID:      info.PCIEBusID,
			PCIEGeneration: info.PCIEGeneration,
			PCIEWidth:      info.PCIEWidth,
			NUMANode:       info.NUMANode,
		})
	}

	if len(snapshot.Accelerator.Devices) == 0 && evidence.ExporterText != "" {
		parsed := devicesnapshot.FromAcceleratorMetrics(evidence.ExporterText)
		if parsed != nil && parsed.Accelerator.Type != v1.StaticNodeAcceleratorTypeCPU {
			snapshot.Accelerator = parsed.Accelerator
			if snapshot.Accelerator.Type == "" {
				snapshot.Accelerator.Type = v1.AcceleratorTypeNVIDIAGPU.String()
			}
		}
	}

	return snapshot
}

func gpuHardwareInfosFromSnapshot(hardware model.AcceleratorHardwareSnapshot) []model.GPUHardwareInfo {
	if len(hardware.Accelerator.Devices) == 0 {
		return nil
	}

	detailsByUUID := map[string]model.AcceleratorHardwareDetails{}

	for _, detail := range hardware.Details {
		if detail.UUID != "" {
			detailsByUUID[detail.UUID] = detail
		}
	}

	result := make([]model.GPUHardwareInfo, 0, len(hardware.Accelerator.Devices))

	for _, device := range hardware.Accelerator.Devices {
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
