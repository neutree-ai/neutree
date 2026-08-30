// Package nvidia implements the Community NVIDIA NodeAgent adapter.
package nvidia

import (
	"cmp"
	"context"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// accelerator is the Community NVIDIA adapter. It owns NVIDIA hardware
// discovery, allocation resolution, and DCGM metric conversion.
type accelerator struct {
	provider      nvidiaHardwareInfoProvider
	processReader nvidiaGPUProcessReader
}

var (
	_ adapter.Accelerator              = (*accelerator)(nil)
	_ adapter.KubernetesAccelerator    = (*accelerator)(nil)
	_ adapter.StaticAccelerator        = (*accelerator)(nil)
	_ adapter.MetricDescriptorProvider = (*accelerator)(nil)
)

// New creates the Community NVIDIA adapter.
func New() adapter.Accelerator {
	return &accelerator{}
}

func (a *accelerator) Type() string {
	return v1.AcceleratorTypeNVIDIAGPU.String()
}

// MetricDescriptors declares the NVIDIA-only metric contract. Generic
// accelerator metric families remain owned by the shared renderer.
func (*accelerator) MetricDescriptors() []adapter.MetricDescriptor {
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

func (a *accelerator) DiscoverHardware(ctx context.Context) (adapter.HardwareSnapshot, error) {
	provider := a.provider
	if provider == nil {
		provider = nvidiaNVMLHardwareInfoProvider{}
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
			UUID:              info.UUID,
			Architecture:      info.Architecture,
			CUDACapability:    info.CUDACapability,
			DriverVersion:     info.DriverVersion,
			CUDADriverVersion: info.CUDADriverVersion,
			PCIEBusID:         info.PCIEBusID,
			PCIEGeneration:    info.PCIEGeneration,
			PCIEWidth:         info.PCIEWidth,
			NUMANode:          info.NUMANode,
		})
	}

	return snapshot, nil
}

func (a *accelerator) BuildKubernetesMetrics(
	ctx context.Context,
	hardwareSnapshot adapter.HardwareSnapshot,
	evidence adapter.KubernetesEvidence,
) (adapter.MetricResult, error) {
	allocations, err := nvidiaKubernetesAllocations(hardwareSnapshot, evidence)
	if err != nil {
		return adapter.MetricResult{}, err
	}

	endpointReplicaUsages := nvidiaVirtualizationUsages(evidence)
	endpointReplicaUsages = append(endpointReplicaUsages, evidence.Common.EndpointReplicaAcceleratorUsages...)
	result := a.buildMetrics(
		ctx,
		hardwareSnapshot,
		evidence.Common,
		nvidiaEndpointAllocations(evidence.Common.Labels, allocations),
		endpointReplicaUsages,
	)
	result.Allocations = allocations

	return result, nil
}

func (a *accelerator) BuildStaticMetrics(
	ctx context.Context,
	hardwareSnapshot adapter.HardwareSnapshot,
	evidence adapter.StaticEvidence,
) (adapter.MetricResult, error) {
	allocations := nvidiaStaticAllocations(hardwareSnapshot, evidence, a.gpuProcesses(ctx))
	result := a.buildMetrics(
		ctx,
		hardwareSnapshot,
		evidence.Common,
		nvidiaEndpointAllocations(evidence.Common.Labels, allocations),
		evidence.Common.EndpointReplicaAcceleratorUsages,
	)
	result.Allocations = allocations

	return result, nil
}

func (a *accelerator) buildMetrics(
	_ context.Context,
	hardwareSnapshot adapter.HardwareSnapshot,
	evidence adapter.CommonEvidence,
	endpointAllocations []model.EndpointAllocation,
	endpointReplicaUsages []adapter.EndpointReplicaAcceleratorUsage,
) adapter.MetricResult {
	labels := evidence.Labels
	if !evidence.ExporterUp {
		return adapter.MetricResult{Samples: nvidiaBuildMetricSamples(
			labels,
			"",
			nil,
			endpointAllocations,
			endpointReplicaUsages,
		)}
	}

	raw := evidence.ExporterText
	hardwareInfos := gpuHardwareInfosFromSnapshot(hardwareSnapshot)

	return adapter.MetricResult{Samples: nvidiaBuildMetricSamples(
		labels,
		raw,
		hardwareInfos,
		endpointAllocations,
		endpointReplicaUsages,
	)}
}

func gpuHardwareInfosFromSnapshot(hardwareSnapshot adapter.HardwareSnapshot) []nvidiaHardwareInfo {
	if len(hardwareSnapshot.Accelerator.Devices) == 0 {
		return nil
	}

	detailsByUUID := make(map[string]adapter.HardwareDetails, len(hardwareSnapshot.Details))

	for _, detail := range hardwareSnapshot.Details {
		if detail.UUID != "" {
			detailsByUUID[detail.UUID] = detail
		}
	}

	result := make([]nvidiaHardwareInfo, 0, len(hardwareSnapshot.Accelerator.Devices))

	for _, device := range hardwareSnapshot.Accelerator.Devices {
		detail := detailsByUUID[device.UUID]
		result = append(result, nvidiaHardwareInfo{
			UUID:              device.UUID,
			Index:             device.ID,
			Product:           cmp.Or(device.ProductModel, device.ProductName),
			Architecture:      detail.Architecture,
			CUDACapability:    detail.CUDACapability,
			DriverVersion:     detail.DriverVersion,
			CUDADriverVersion: detail.CUDADriverVersion,
			MemoryTotalMiB:    fmt.Sprintf("%d", device.MemoryMiB),
			PCIEBusID:         detail.PCIEBusID,
			PCIEGeneration:    detail.PCIEGeneration,
			PCIEWidth:         detail.PCIEWidth,
			NUMANode:          detail.NUMANode,
		})
	}

	return result
}
