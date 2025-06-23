package v1

import (
	corev1 "k8s.io/api/core/v1"
)

const (
	GetNodeAcceleratorPath        = "/v1/node/accelerator"
	GetNodeRuntimeConfigPath      = "/v1/node/runtime-config"
	GetContainerAcceleratorPath   = "/v1/container/accelerator"
	GetContainerRuntimeConfigPath = "/v1/container/runtime-config"
	GetSupportEnginesPath         = "/v1/support-engines"
	PingPath                      = "/v1/ping"
	RegisterPath                  = "/v1/register"
)

type Accelerator struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
}

type GetNodeAcceleratorRequest struct {
	NodeIp  string `json:"node_ip"`
	SSHAuth Auth   `json:"ssh_auth"`
}

type GetNodeAcceleratorResponse struct {
	Accelerators []Accelerator `json:"accelerators"`
}

type GetNodeRuntimeConfigRequest struct {
	NodeIp  string `json:"node_ip"`
	SSHAuth Auth   `json:"ssh_auth"`
}

type GetNodeRuntimeConfigResponse struct {
	RuntimeConfig RuntimeConfig `json:"runtime_config"`
}

type GetContainerAcceleratorRequest struct {
	Container corev1.Container `json:"container"`
}

type GetContainerAcceleratorResponse struct {
	Accelerators []Accelerator `json:"accelerators"`
}

type GetContainerRuntimeConfigRequest struct {
	Container corev1.Container `json:"container"`
}

type GetContainerRuntimeConfigResponse struct {
	RuntimeConfig RuntimeConfig `json:"runtime_config"`
}

type RuntimeConfig struct {
	ImageSuffix string            `json:"image_suffix"`
	Env         map[string]string `json:"env"`
	Runtime     string            `json:"runtime"`
	Options     []string          `json:"options"`
}

type GetSupportEnginesResponse struct {
	Engines []*Engine `json:"engines"`
}

type RegisterRequest struct {
	ResourceName string `json:"resource_name"`
	Endpoint     string `json:"endpoint"`
	Version      string `json:"version"`
}
