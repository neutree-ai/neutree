package model

import (
	"net/http"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	SourceNodeAgent     = "neutree-node-agent"
	WorkloadRoleBackend = "backend"
)

type CanonicalLabels struct {
	Workspace         string
	NeutreeCluster    string
	StaticNodeCluster string
	ClusterType       string
	Node              string
	NodeIP            string
	NodeRole          string
}

type ScrapeResult struct {
	Target string
	Up     bool
	Body   string
	Error  string
}

type NodeDeviceSnapshot struct {
	Accelerator v1.StaticNodeAcceleratorStatus  `json:"accelerator,omitempty"`
	Allocations []v1.StaticNodeAllocationStatus `json:"allocations,omitempty"`
}

type DeviceSnapshotProvider interface {
	DeviceSnapshot(r *http.Request) (*NodeDeviceSnapshot, error)
}

type DeviceSnapshotProviderFunc func(r *http.Request) (*NodeDeviceSnapshot, error)

func (f DeviceSnapshotProviderFunc) DeviceSnapshot(r *http.Request) (*NodeDeviceSnapshot, error) {
	return f(r)
}

type EndpointAllocation struct {
	Workspace  string
	Cluster    string
	Endpoint   string
	InstanceID string
	ReplicaID  string
	NodeID     string
	Devices    []v1.DeviceAllocation
}

type EndpointReplicaRuntimeUsage struct {
	Workspace             string
	Cluster               string
	Endpoint              string
	InstanceID            string
	ReplicaID             string
	NodeID                string
	WorkloadRole          string
	Container             string
	ContainerID           string
	Engine                string
	EngineVersion         string
	CPUUsageSeconds       float64
	MemoryUsageBytes      *float64
	MemoryWorkingSetBytes *float64
	CPULimitCores         *float64
	MemoryLimitBytes      *float64
}

type EndpointReplicaGPUUsage struct {
	Workspace            string
	Cluster              string
	Endpoint             string
	InstanceID           string
	ReplicaID            string
	NodeID               string
	Container            string
	GPUUUID              string
	AcceleratorType      string
	AcceleratorIndex     string
	VDeviceIndex         string
	Product              string
	MemoryAllocatedBytes *float64
	MemoryUsedBytes      *float64
	UtilizationRatio     *float64
}

type GPUHardwareInfo struct {
	UUID              string
	Index             string
	MinorNumber       string
	Product           string
	Architecture      string
	CUDACapability    string
	DriverVersion     string
	CUDADriverVersion string
	MemoryTotalMiB    string
	NVLink            string
	NVSwitch          string
	PCIEBusID         string
	PCIEGeneration    string
	PCIEWidth         string
	NUMANode          string
}

type PodResource struct {
	Namespace  string
	Name       string
	Containers []ContainerDevices
}

type ContainerDevices struct {
	ResourceName string
	DeviceIDs    []string
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}
