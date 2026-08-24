// Package adapter defines the public NodeAgent accelerator extension contract.
package adapter

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Accelerator identifies an accelerator implementation and discovers the
// physical inventory visible on the local node.
type Accelerator interface {
	Type() string
	DiscoverHardware(context.Context) (HardwareSnapshot, error)
}

// KubernetesAccelerator builds metrics from Kubernetes-specific raw evidence.
type KubernetesAccelerator interface {
	Accelerator
	BuildKubernetesMetrics(context.Context, HardwareSnapshot, KubernetesEvidence) (MetricResult, error)
}

// StaticAccelerator builds metrics from static Ray-cluster raw evidence.
type StaticAccelerator interface {
	Accelerator
	BuildStaticMetrics(context.Context, HardwareSnapshot, StaticEvidence) (MetricResult, error)
}

// MetricDescriptorProvider optionally declares adapter-owned metric series.
type MetricDescriptorProvider interface {
	MetricDescriptors() []MetricDescriptor
}

// CanonicalLabels carries shared NodeAgent labels without exposing an internal
// collector model to external adapters.
type CanonicalLabels struct {
	Workspace         string
	NeutreeCluster    string
	StaticNodeCluster string
	ClusterType       string
	Node              string
	NodeIP            string
	NodeRole          string
}

// HardwareSnapshot is the local physical accelerator inventory plus optional
// immutable details used by adapters to correlate exporter data.
type HardwareSnapshot struct {
	Accelerator v1.StaticNodeAcceleratorStatus
	Details     []HardwareDetails
}

// HardwareDetails holds stable hardware fields not represented in the API
// inventory object.
type HardwareDetails struct {
	UUID           string
	Architecture   string
	DriverVersion  string
	PCIEBusID      string
	PCIEGeneration string
	PCIEWidth      string
	NUMANode       string
}

// CommonEvidence is shared by Kubernetes and static topology evidence.
type CommonEvidence struct {
	ExporterText                     string
	ExporterUp                       bool
	EndpointReplicaAcceleratorUsages []EndpointReplicaAcceleratorUsage
	Labels                           CanonicalLabels
}

// EndpointReplicaAcceleratorUsage is a raw per-replica accelerator usage
// observation. The host preserves this data without interpreting a vendor's
// allocation or metric semantics.
type EndpointReplicaAcceleratorUsage struct {
	Workspace        string
	Cluster          string
	Endpoint         string
	InstanceID       string
	ReplicaID        string
	NodeID           string
	Container        string
	AcceleratorUUID  string
	AcceleratorType  string
	AcceleratorIndex string
	VDeviceIndex     string
	Product          string
	MemoryUsedBytes  *float64
	UtilizationRatio *float64
}

// KubernetesEvidence contains raw Kubernetes allocation observations. The
// host does not interpret resource names, IDs, labels, or annotations.
type KubernetesEvidence struct {
	Common              CommonEvidence
	AllocationAvailable bool
	PodResources        []PodResource
	EndpointPods        []EndpointPodEvidence
}

// PodResource is a raw kubelet PodResources observation.
type PodResource struct {
	Namespace  string
	Name       string
	Containers []ContainerDevices
}

// ContainerDevices preserves a kubelet resource name and its device IDs.
type ContainerDevices struct {
	ResourceName string
	DeviceIDs    []string
}

// EndpointPodEvidence is a copied local endpoint Pod identity and metadata.
type EndpointPodEvidence struct {
	Namespace   string
	Name        string
	UID         string
	NodeName    string
	Labels      map[string]string
	Annotations map[string]string
}

// StaticEvidence contains raw Ray and process observations for static
// clusters. Vendor-specific process and resource semantics remain adapter
// owned.
type StaticEvidence struct {
	Common              CommonEvidence
	AllocationAvailable bool
	RayEvidence         RayEvidence
}

// RayEvidence contains raw actor and process topology observations.
type RayEvidence struct {
	Actors         []RayActor
	Replicas       []RayReplica
	ActorProcesses map[int]ProcessInfo
}

// RayActor is the public subset of a Ray Dashboard actor observation needed
// by accelerator adapters.
type RayActor struct {
	ActorID           string
	ClassName         string
	State             string
	Name              string
	NodeID            string
	PID               int
	RequiredResources map[string]float64
	StartTime         int64
	EndTime           int64
}

// RayReplica preserves the Ray Serve replica-to-actor association observed by
// the host. Accelerator allocation semantics are still adapter owned.
type RayReplica struct {
	Workspace   string
	Endpoint    string
	Deployment  string
	ActorID     string
	ReplicaID   string
	NodeID      string
	GPUQuantity float64
}

// ProcessInfo is an uninterpreted local process observation.
type ProcessInfo struct {
	PID            int
	ParentPID      int
	DescendantPIDs []int
	Environment    map[string]string
}

// MetricResult is the only adapter output accepted by the host. Inventory is
// returned by DiscoverHardware; this result contains workload allocations and
// canonical metric samples.
type MetricResult struct {
	Allocations []v1.StaticNodeAllocationStatus
	Samples     []Sample
}

// Sample is one canonical Neutree metric series.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// MetricDescriptor declares an adapter-owned metric name and label contract.
type MetricDescriptor struct {
	Name               string
	LabelNames         []string
	RequiredLabelNames []string
}

// Clone returns a deep copy suitable for passing across the host boundary.
func (s HardwareSnapshot) Clone() HardwareSnapshot {
	result := HardwareSnapshot{Accelerator: s.Accelerator}
	if len(s.Accelerator.Devices) > 0 {
		result.Accelerator.Devices = make([]v1.StaticNodeAcceleratorDeviceStatus, 0, len(s.Accelerator.Devices))
		for _, device := range s.Accelerator.Devices {
			copied := device
			if device.MinorNumber != nil {
				minor := *device.MinorNumber
				copied.MinorNumber = &minor
			}
			result.Accelerator.Devices = append(result.Accelerator.Devices, copied)
		}
	}
	result.Details = append([]HardwareDetails(nil), s.Details...)

	return result
}

// Clone returns a deep copy of common evidence.
func (e CommonEvidence) Clone() CommonEvidence {
	result := CommonEvidence{
		ExporterText: e.ExporterText,
		ExporterUp:   e.ExporterUp,
		Labels:       e.Labels,
	}
	result.EndpointReplicaAcceleratorUsages = cloneEndpointReplicaAcceleratorUsages(
		e.EndpointReplicaAcceleratorUsages,
	)

	return result
}

// Clone returns a deep copy of Kubernetes evidence.
func (e KubernetesEvidence) Clone() KubernetesEvidence {
	result := KubernetesEvidence{
		Common:              e.Common.Clone(),
		AllocationAvailable: e.AllocationAvailable,
	}
	result.PodResources = make([]PodResource, 0, len(e.PodResources))
	for _, pod := range e.PodResources {
		copied := PodResource{Namespace: pod.Namespace, Name: pod.Name}
		copied.Containers = make([]ContainerDevices, 0, len(pod.Containers))
		for _, container := range pod.Containers {
			copied.Containers = append(copied.Containers, ContainerDevices{
				ResourceName: container.ResourceName,
				DeviceIDs:    append([]string(nil), container.DeviceIDs...),
			})
		}
		result.PodResources = append(result.PodResources, copied)
	}
	result.EndpointPods = make([]EndpointPodEvidence, 0, len(e.EndpointPods))
	for _, pod := range e.EndpointPods {
		result.EndpointPods = append(result.EndpointPods, EndpointPodEvidence{
			Namespace:   pod.Namespace,
			Name:        pod.Name,
			UID:         pod.UID,
			NodeName:    pod.NodeName,
			Labels:      copyStringMap(pod.Labels),
			Annotations: copyStringMap(pod.Annotations),
		})
	}

	return result
}

// Clone returns a deep copy of static evidence.
func (e StaticEvidence) Clone() StaticEvidence {
	result := StaticEvidence{
		Common:              e.Common.Clone(),
		AllocationAvailable: e.AllocationAvailable,
		RayEvidence: RayEvidence{
			Actors:         make([]RayActor, 0, len(e.RayEvidence.Actors)),
			Replicas:       make([]RayReplica, 0, len(e.RayEvidence.Replicas)),
			ActorProcesses: make(map[int]ProcessInfo, len(e.RayEvidence.ActorProcesses)),
		},
	}
	for _, actor := range e.RayEvidence.Actors {
		result.RayEvidence.Actors = append(result.RayEvidence.Actors, RayActor{
			ActorID:           actor.ActorID,
			ClassName:         actor.ClassName,
			State:             actor.State,
			Name:              actor.Name,
			NodeID:            actor.NodeID,
			PID:               actor.PID,
			RequiredResources: copyFloat64Map(actor.RequiredResources),
			StartTime:         actor.StartTime,
			EndTime:           actor.EndTime,
		})
	}
	result.RayEvidence.Replicas = append(result.RayEvidence.Replicas, e.RayEvidence.Replicas...)
	for pid, process := range e.RayEvidence.ActorProcesses {
		result.RayEvidence.ActorProcesses[pid] = ProcessInfo{
			PID:            process.PID,
			ParentPID:      process.ParentPID,
			DescendantPIDs: append([]int(nil), process.DescendantPIDs...),
			Environment:    copyStringMap(process.Environment),
		}
	}

	return result
}

// Clone returns a deep copy of an adapter result before host validation.
func (r MetricResult) Clone() MetricResult {
	result := MetricResult{
		Allocations: cloneAllocations(r.Allocations),
		Samples:     make([]Sample, 0, len(r.Samples)),
	}
	for _, sample := range r.Samples {
		result.Samples = append(result.Samples, Sample{
			Name:   sample.Name,
			Labels: copyStringMap(sample.Labels),
			Value:  sample.Value,
		})
	}

	return result
}

// CloneMetricDescriptors returns a copy that callers may safely retain.
func CloneMetricDescriptors(descriptors []MetricDescriptor) []MetricDescriptor {
	result := make([]MetricDescriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		result = append(result, MetricDescriptor{
			Name:               descriptor.Name,
			LabelNames:         append([]string(nil), descriptor.LabelNames...),
			RequiredLabelNames: append([]string(nil), descriptor.RequiredLabelNames...),
		})
	}

	return result
}

func cloneAllocations(allocations []v1.StaticNodeAllocationStatus) []v1.StaticNodeAllocationStatus {
	result := make([]v1.StaticNodeAllocationStatus, 0, len(allocations))
	for _, allocation := range allocations {
		copied := allocation
		copied.Devices = make([]v1.DeviceAllocation, 0, len(allocation.Devices))
		for _, device := range allocation.Devices {
			deviceCopy := device
			if device.Order != nil {
				order := *device.Order
				deviceCopy.Order = &order
			}
			copied.Devices = append(copied.Devices, deviceCopy)
		}
		result = append(result, copied)
	}

	return result
}

func copyStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}

	return result
}

func copyFloat64Map(input map[string]float64) map[string]float64 {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]float64, len(input))
	for key, value := range input {
		result[key] = value
	}

	return result
}

func cloneEndpointReplicaAcceleratorUsages(
	usages []EndpointReplicaAcceleratorUsage,
) []EndpointReplicaAcceleratorUsage {
	result := make([]EndpointReplicaAcceleratorUsage, 0, len(usages))
	for _, usage := range usages {
		copied := usage
		if usage.MemoryUsedBytes != nil {
			memoryUsedBytes := *usage.MemoryUsedBytes
			copied.MemoryUsedBytes = &memoryUsedBytes
		}
		if usage.UtilizationRatio != nil {
			utilizationRatio := *usage.UtilizationRatio
			copied.UtilizationRatio = &utilizationRatio
		}
		result = append(result, copied)
	}

	return result
}
