package v1

import (
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

const (
	TextGenerationModelTask = "text-generation"
	TextEmbeddingModelTask  = "text-embedding"
	TextRerankModelTask     = "text-rerank"
)

// EngineVersion represents a specific version of an engine with its configuration schema,
// deployment templates, and supported accelerators.
//
// EngineVersion is distributed through EngineVersion packages that contain:
//   - Container images for different accelerators
//   - ValuesSchema: JSON Schema for configuration parameters
//   - DeployTemplate: Deployment configurations for different cluster types and modes
//   - Images: Mapping of accelerator types to container images (replaces SupportAccelerators)
//
// Example usage:
//
//	version := &EngineVersion{
//	    Version: "v0.5.0",
//	    ValuesSchema: map[string]interface{}{
//	        "type": "object",
//	        "properties": map[string]interface{}{
//	            "gpu_memory_utilization": map[string]interface{}{
//	                "type": "number",
//	                "default": 0.9,
//	            },
//	        },
//	    },
//	    DeployTemplate: map[string]map[string]string{
//	        "kubernetes": {
//	            "default": "...",
//	        },
//	    },
//	    Images: map[string]*EngineImage{
//	        "nvidia_gpu": {
//	            ImageName: "vllm",
//	            Tag:       "v0.5.0",
//	        },
//	        "amd_gpu": {
//	            ImageName: "vllm-rocm",
//	            Tag:       "v0.5.0",
//	        },
//	    },
//	}
type EngineVersion struct {
	// Version is the version identifier (e.g., "v0.5.0", "v1.0.0")
	Version string `json:"version,omitempty" yaml:"version,omitempty"`

	// ValuesSchema is a JSON Schema defining the configuration parameters for this engine version.
	// It follows the JSON Schema specification and is used to validate and provide defaults
	// for engine configuration values.
	//
	// Example:
	//  {
	//    "type": "object",
	//    "properties": {
	//      "gpu_memory_utilization": {
	//        "type": "number",
	//        "description": "GPU memory utilization ratio",
	//        "default": 0.9,
	//        "minimum": 0.1,
	//        "maximum": 1.0
	//      }
	//    }
	//  }
	ValuesSchema map[string]interface{} `json:"values_schema,omitempty" yaml:"values_schema,omitempty"`

	// DeployTemplate contains Base64-encoded deployment templates for different cluster types and modes.
	// The first level key represents the cluster type (e.g., "kubernetes", "ssh").
	// The second level key represents the deployment mode (e.g., "default", "pd", "tp").
	// Values are Base64-encoded YAML template strings to avoid JSON escaping issues.
	//
	// Example:
	//  DeployTemplate: map[string]map[string]string{
	//      "kubernetes": {
	//          "default": "YXBpVmVyc2lvbjogYXBwcy92MQpraW5kOiBEZXBsb3ltZW50...",
	//      },
	//  }
	DeployTemplate map[string]map[string]string `json:"deploy_template,omitempty" yaml:"deploy_template,omitempty"`

	// Images contains the mapping of accelerator types to their corresponding container images.
	// Each accelerator type can have a different image (e.g., CUDA for NVIDIA, ROCm for AMD).
	// The keys of this map represent the supported accelerator types for this engine version.
	//
	// Example:
	//  {
	//    "nvidia_gpu": {
	//      "image_name": "vllm",
	//      "tag": "v0.5.0"
	//    },
	//    "amd_gpu": {
	//      "image_name": "vllm-rocm",
	//      "tag": "v0.5.0"
	//    },
	//    "cpu": {
	//      "image_name": "vllm-cpu",
	//      "tag": "v0.5.0"
	//    }
	//  }
	Images map[string]*EngineImage `json:"images,omitempty" yaml:"images,omitempty"`

	// SupportedTasks lists the tasks supported by this engine version
	//
	// Example:
	//  SupportedTasks: []string{
	//    "text-generate",
	//    "text-embedding",
	//  },
	SupportedTasks []string `json:"supported_tasks,omitempty" yaml:"supported_tasks,omitempty"`
}

// EngineImage describes the container image information for a specific accelerator type
type EngineImage struct {
	// ImageName is the full image reference without tag
	// Example: "neutree/vllm-cuda", "neutree/vllm-rocm"
	ImageName string `json:"image_name,omitempty" yaml:"image_name,omitempty"`

	// Tag is the image tag
	// Example: "v0.5.0", "latest"
	Tag string `json:"tag,omitempty" yaml:"tag,omitempty"`
}

type EngineSpec struct {
	Versions       []*EngineVersion `json:"versions,omitempty"`
	SupportedTasks []string         `json:"supported_tasks,omitempty"`
}

type EnginePhase string

const (
	EnginePhasePending EnginePhase = "Pending"
	EnginePhaseCreated EnginePhase = "Created"
	EnginePhaseDeleted EnginePhase = "Deleted"
	EnginePhaseFailed  EnginePhase = "Failed"
)

type EngineStatus struct {
	Phase              EnginePhase `json:"phase,omitempty"`
	LastTransitionTime string      `json:"last_transition_time,omitempty"`
	ErrorMessage       string      `json:"error_message,omitempty"`
}

type Engine struct {
	ID         int           `json:"id,omitempty"`
	APIVersion string        `json:"api_version,omitempty"`
	Kind       string        `json:"kind,omitempty"`
	Metadata   *Metadata     `json:"metadata,omitempty"`
	Spec       *EngineSpec   `json:"spec,omitempty"`
	Status     *EngineStatus `json:"status,omitempty"`
}

func (obj *Engine) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *Engine) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *Engine) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *Engine) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *Engine) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *Engine) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *Engine) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *Engine) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *Engine) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *Engine) GetSpec() interface{} {
	return obj.Spec
}

func (obj *Engine) GetStatus() interface{} {
	return obj.Status
}

func (obj *Engine) GetKind() string {
	return obj.Kind
}

func (obj *Engine) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *Engine) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *Engine) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *Engine) GetMetadata() interface{} {
	return obj.Metadata
}

// EngineList is a list of Engine resources
type EngineList struct {
	Kind  string   `json:"kind"`
	Items []Engine `json:"items"`
}

func (in *EngineList) GetKind() string {
	return in.Kind
}

func (in *EngineList) SetKind(kind string) {
	in.Kind = kind
}

func (in *EngineList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *EngineList) SetItems(objs []scheme.Object) {
	items := make([]Engine, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*Engine) //nolint:errcheck
	}

	in.Items = items
}

// GetImageForAccelerator returns the image information for a specific accelerator type
// If the accelerator type is not found, it returns nil
func (ev *EngineVersion) GetImageForAccelerator(acceleratorType string) *EngineImage {
	if ev.Images == nil {
		return nil
	}

	return ev.Images[acceleratorType]
}

// GetSupportedAccelerators returns a list of supported accelerator types
// The list is derived from the keys of the Images map
func (ev *EngineVersion) GetSupportedAccelerators() []string {
	if ev.Images == nil {
		return []string{}
	}

	accelerators := make([]string, 0, len(ev.Images))
	for acceleratorType := range ev.Images {
		accelerators = append(accelerators, acceleratorType)
	}

	return accelerators
}

// SupportsAccelerator checks if the engine version supports a specific accelerator type
func (ev *EngineVersion) SupportsAccelerator(acceleratorType string) bool {
	if ev.Images == nil {
		return false
	}

	_, exists := ev.Images[acceleratorType]

	return exists
}

// SetImage sets the image information for a specific accelerator type
func (ev *EngineVersion) SetImage(acceleratorType string, imageName, tag string) {
	if ev.Images == nil {
		ev.Images = make(map[string]*EngineImage)
	}

	ev.Images[acceleratorType] = &EngineImage{
		ImageName: imageName,
		Tag:       tag,
	}
}

// HasImageForAccelerator checks if an image is configured for the specified accelerator type
func (ev *EngineVersion) HasImageForAccelerator(acceleratorType string) bool {
	return ev.GetImageForAccelerator(acceleratorType) != nil
}

// GetFullImagePath returns the image name and tag separately for a specific accelerator type
// Returns empty strings if the accelerator type is not found
func (img *EngineImage) GetFullImagePath() (imageName string, tag string) {
	if img == nil {
		return "", ""
	}

	return img.ImageName, img.Tag
}

// GetDeployTemplate retrieves the deployment template for a specific cluster type and mode.
// It automatically handles Base64 decoding.
// The template is stored as Base64-encoded string to avoid JSON escaping issues.
func (ev *EngineVersion) GetDeployTemplate(clusterType, mode string) (string, error) {
	if ev.DeployTemplate == nil {
		return "", fmt.Errorf("deploy templates not configured")
	}

	clusterModes := ev.DeployTemplate[clusterType]
	if clusterModes == nil {
		return "", fmt.Errorf("cluster type %s not found in deploy templates", clusterType)
	}

	encoded := clusterModes[mode]
	if encoded == "" {
		return "", fmt.Errorf("deploy mode %s not found for cluster type %s", mode, clusterType)
	}

	// Decode from Base64
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode template for %s/%s: %w", clusterType, mode, err)
	}

	return string(decoded), nil
}

// SetDeployTemplate sets the deployment template using Base64 encoding.
// This avoids JSON escaping issues with special characters in YAML templates.
func (ev *EngineVersion) SetDeployTemplate(clusterType, mode, content string) {
	if ev.DeployTemplate == nil {
		ev.DeployTemplate = make(map[string]map[string]string)
	}

	if ev.DeployTemplate[clusterType] == nil {
		ev.DeployTemplate[clusterType] = make(map[string]string)
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	ev.DeployTemplate[clusterType][mode] = encoded
}

// HasDeployTemplate checks if a deployment template exists for the given cluster type and mode
func (ev *EngineVersion) HasDeployTemplate(clusterType, mode string) bool {
	if ev.DeployTemplate == nil {
		return false
	}

	clusterModes := ev.DeployTemplate[clusterType]
	if clusterModes == nil {
		return false
	}

	return clusterModes[mode] != ""
}

// GetDeployTemplateRaw returns the raw Base64-encoded template without decoding.
// This is useful for package export/import operations.
func (ev *EngineVersion) GetDeployTemplateRaw(clusterType, mode string) string {
	if ev.DeployTemplate == nil {
		return ""
	}

	clusterModes := ev.DeployTemplate[clusterType]
	if clusterModes == nil {
		return ""
	}

	return clusterModes[mode]
}
