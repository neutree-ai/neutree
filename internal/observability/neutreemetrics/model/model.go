package model

import v1 "github.com/neutree-ai/neutree/api/v1"

const (
	SourceNodeAgent     = "neutree-node-agent"
	WorkloadRoleBackend = "backend"
)

type ScrapeResult struct {
	Target string
	Up     bool
	Body   string
	Error  string
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
