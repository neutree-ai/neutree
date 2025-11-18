package v1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	GetNodeAcceleratorPath        = "/v1/node/accelerator"
	GetNodeRuntimeConfigPath      = "/v1/node/runtime-config"
	GetContainerAcceleratorPath   = "/v1/container/accelerator"
	GetContainerRuntimeConfigPath = "/v1/container/runtime-config"
	GetSupportEnginesPath         = "/v1/support-engines"
	GetClusterResourcesPath       = "/v1/cluster/resources"
	PingPath                      = "/v1/ping"

	// Resource conversion API paths
	ConvertToRayPath        = "/v1/resource/convert-to-ray"
	ConvertToKubernetesPath = "/v1/resource/convert-to-kubernetes"

	// Resource Parse API paths
	ParseFromRayPath        = "/v1/resource/parse-from-ray"
	ParseFromKubernetesPath = "/v1/resource/parse-from-kubernetes"

	PluginAPIGroupPath = "/v1/plugin"
	RegisterPath       = PluginAPIGroupPath + "/register"
)

type AcceleratorType string

// Accelerator type constants
const (
	AcceleratorTypeNVIDIAGPU AcceleratorType = "nvidia_gpu"
	AcceleratorTypeAMDGPU    AcceleratorType = "amd_gpu"
)

type AcceleratorProduct string

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
	ResourceName string `json:"resource_name"` // Accelerator resource type (e.g., "nvidia_gpu", "amd_gpu")
	Endpoint     string `json:"endpoint"`
	Version      string `json:"version"`
}

type ParseFromKubernetesRequest struct {
	Resource map[corev1.ResourceName]resource.Quantity `json:"resource"`
	Labels   map[string]string                         `json:"labels"`
}

type ParseFromRayRequest struct {
	Resource map[string]float64 `json:"resource"`
}
