package adapter

import (
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/devicesnapshot"
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
type nvidiaAccelerator struct{}

func (a *nvidiaAccelerator) Type() string {
	return v1.AcceleratorTypeNVIDIAGPU.String()
}

func (a *nvidiaAccelerator) BuildMetrics(
	_ context.Context,
	evidence AcceleratorEvidence,
) (AcceleratorMetricResult, error) {
	if evidence.AcceleratorType != "" && evidence.AcceleratorType != a.Type() {
		return AcceleratorMetricResult{}, fmt.Errorf(
			"adapter %q selected for accelerator type %q",
			a.Type(),
			evidence.AcceleratorType,
		)
	}

	if !evidence.ExporterUp {
		// Physical evidence and scheduler evidence degrade independently: the
		// explicit replica GPU usages come from the scheduler-side usage
		// provider and are still emitted when the accelerator exporter is down,
		// matching the legacy normalizer behavior.
		return AcceleratorMetricResult{
			Samples: normalizer.NormalizeEndpointReplicaGPUUsageSamples(
				evidence.Labels,
				evidence.EndpointReplicaGPUUsages,
				evidence.EndpointAllocations,
				nil,
			),
		}, nil
	}

	labels := evidence.Labels
	raw := evidence.ExporterText
	acceleratorIndexes := normalizer.AcceleratorIndexesByUUID(raw, evidence.GPUHardwareInfos)

	samples := make([]normalizer.Sample, 0, len(evidence.EndpointAllocations)*2+8)
	samples = append(samples, normalizer.NormalizeAcceleratorSamples(labels, raw)...)
	samples = append(samples, normalizer.NormalizeNodeGPUSamples(labels, raw, evidence.EndpointAllocations)...)
	samples = append(samples, normalizer.NormalizeGPUHardwareInfoSamples(labels, evidence.GPUHardwareInfos, raw)...)
	samples = append(samples, normalizer.NormalizeEndpointAllocationSamples(
		labels,
		evidence.EndpointAllocations,
		evidence.EndpointReplicaGPUUsages,
		acceleratorIndexes,
		raw,
	)...)
	samples = append(samples, normalizer.NormalizeEndpointReplicaGPUUsageFromDCGMSamples(
		labels,
		raw,
		evidence.EndpointAllocations,
		evidence.EndpointReplicaGPUUsages,
	)...)
	samples = append(samples, normalizer.NormalizeEndpointReplicaGPUUsageSamples(
		labels,
		evidence.EndpointReplicaGPUUsages,
		evidence.EndpointAllocations,
		acceleratorIndexes,
	)...)

	return AcceleratorMetricResult{
		DeviceSnapshots: deviceSnapshotsFromEvidence(evidence),
		Samples:         samples,
	}, nil
}

func deviceSnapshotsFromEvidence(evidence AcceleratorEvidence) []v1.DeviceAllocation {
	snapshot := devicesnapshot.FromAcceleratorMetrics(evidence.ExporterText)
	if snapshot == nil || snapshot.Accelerator.Type == v1.StaticNodeAcceleratorTypeCPU {
		return nil
	}

	result := make([]v1.DeviceAllocation, 0, len(snapshot.Accelerator.Devices))

	for _, device := range snapshot.Accelerator.Devices {
		result = append(result, v1.DeviceAllocation{
			UUID:      device.UUID,
			Product:   model.FirstNonEmpty(device.ProductModel, device.ProductName),
			MemoryMiB: device.MemoryMiB,
			NodeID:    evidence.Labels.Node,
		})
	}

	return result
}
