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

type DeviceSnapshotProvider interface {
	DeviceSnapshot(r *http.Request) (*v1.NodeDeviceSnapshot, error)
}

type DeviceSnapshotProviderFunc func(r *http.Request) (*v1.NodeDeviceSnapshot, error)

func (f DeviceSnapshotProviderFunc) DeviceSnapshot(r *http.Request) (*v1.NodeDeviceSnapshot, error) {
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
	Workspace        string
	Cluster          string
	Endpoint         string
	InstanceID       string
	ReplicaID        string
	NodeID           string
	Container        string
	GPUUUID          string
	AcceleratorType  string
	AcceleratorIndex string
	VDeviceIndex     string
	Product          string
	MemoryUsedBytes  *float64
	UtilizationRatio *float64
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

type AcceleratorHardwareSnapshot struct {
	Accelerator v1.StaticNodeAcceleratorStatus
	Details     []AcceleratorHardwareDetails
}

type AcceleratorHardwareDetails struct {
	UUID           string
	Architecture   string
	DriverVersion  string
	PCIEBusID      string
	PCIEGeneration string
	PCIEWidth      string
	NUMANode       string
}

// ProcessInfo is an uninterpreted process observation passed from the shared
// static-cluster collector to an accelerator adapter.
type ProcessInfo struct {
	PID            int
	ParentPID      int
	DescendantPIDs []int
	Environment    map[string]string
}

func (s AcceleratorHardwareSnapshot) Clone() AcceleratorHardwareSnapshot {
	cloned := AcceleratorHardwareSnapshot{
		Accelerator: s.Accelerator,
	}
	if len(s.Accelerator.Devices) > 0 {
		cloned.Accelerator.Devices = make([]v1.StaticNodeAcceleratorDeviceStatus, 0, len(s.Accelerator.Devices))

		for _, device := range s.Accelerator.Devices {
			copied := device

			if device.MinorNumber != nil {
				minor := *device.MinorNumber
				copied.MinorNumber = &minor
			}

			cloned.Accelerator.Devices = append(cloned.Accelerator.Devices, copied)
		}
	}

	if len(s.Details) > 0 {
		cloned.Details = append([]AcceleratorHardwareDetails{}, s.Details...)
	}

	return cloned
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
