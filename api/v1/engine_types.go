package v1

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

const (
	TextGenerationModelTask = "text-generation"
	TextEmbeddingModelTask  = "text-embedding"
	TextRerankModelTask     = "text-rerank"

	// Engine name constants
	EngineNameVLLM     = "vllm"
	EngineNameLlamaCpp = "llama-cpp"
	EngineNameSGLang   = "sglang"
)

// IsBuiltInModelDownloaderEngine reports whether engine uses Neutree's
// built-in model downloader.
func IsBuiltInModelDownloaderEngine(name string) bool {
	switch name {
	case EngineNameVLLM, EngineNameLlamaCpp, EngineNameSGLang:
		return true
	default:
		return false
	}
}

// knownModelTasks is the canonical set of task identifiers consumed by Neutree
// engine deploy templates (vLLM / llama-cpp). Values outside this set are
// silently ignored by the templates and must be rejected at ingestion time
// (see internal/cli/packageimport for import-side enforcement).
var knownModelTasks = map[string]struct{}{
	TextGenerationModelTask: {},
	TextEmbeddingModelTask:  {},
	TextRerankModelTask:     {},
}

// IsKnownModelTask reports whether task is one of the values understood by
// Neutree's engine deploy templates. Use this at any boundary that ingests
// user-supplied task identifiers (CLI imports, API server validation).
func IsKnownModelTask(task string) bool {
	_, ok := knownModelTasks[task]
	return ok
}

// KnownModelTasks returns the canonical set of task identifiers in a stable
// sorted order. Single source of truth shared by IsKnownModelTask and any
// caller that needs to render the accepted set (e.g. error messages),
// preventing drift between validation and what we tell users is accepted.
func KnownModelTasks() []string {
	tasks := make([]string, 0, len(knownModelTasks))
	for t := range knownModelTasks {
		tasks = append(tasks, t)
	}

	sort.Strings(tasks)

	return tasks
}

// Playground interaction modes. A mode names the interaction surface an engine
// can serve, which is deliberately not the same axis as a model task: several
// engines expose a chat-shaped playground without advertising
// TextGenerationModelTask (document-extraction engines such as MinerU, for
// example), so the UI must not derive the playground from the task.
const (
	PlaygroundModeChat      = "chat"
	PlaygroundModeEmbedding = "embedding"
	PlaygroundModeRerank    = "rerank"
)

// knownPlaygroundModes is the canonical set of interaction modes the console
// knows how to render. Mirrors knownModelTasks: values outside this set have no
// UI implementation, so they are rejected at registration time rather than
// silently ignored.
var knownPlaygroundModes = map[string]struct{}{
	PlaygroundModeChat:      {},
	PlaygroundModeEmbedding: {},
	PlaygroundModeRerank:    {},
}

// IsKnownPlaygroundMode reports whether mode is one of the interaction modes the
// console can render.
func IsKnownPlaygroundMode(mode string) bool {
	_, ok := knownPlaygroundModes[mode]
	return ok
}

// KnownPlaygroundModes returns the canonical set of interaction modes in a
// stable sorted order. Single source of truth shared by IsKnownPlaygroundMode
// and any caller that needs to render the accepted set (e.g. error messages).
func KnownPlaygroundModes() []string {
	modes := make([]string, 0, len(knownPlaygroundModes))
	for m := range knownPlaygroundModes {
		modes = append(modes, m)
	}

	sort.Strings(modes)

	return modes
}

// Defaults applied when a capability is declared but leaves a field unset, and
// when an engine version declares no capabilities at all. They encode the
// behaviour Neutree had before the capability protocol existed: the Kubernetes
// vmagent `neutree-inference` job scraped every `app=inference` pod on a fixed
// :8000, and the console showed the Playground tab unconditionally.
const (
	DefaultMetricsExportPort = 8000
	DefaultMetricsExportPath = "/metrics"
)

// MetricsExportCapability declares whether an engine version exposes Prometheus
// metrics, and where.
//
// Enabled is a non-pointer bool on purpose: a declared capability always states
// its answer, so `{"enabled": false}` is how an engine opts out. "Not declared"
// is expressed one level up, by leaving EngineCapabilities.MetricsExport nil.
type MetricsExportCapability struct {
	// Enabled reports whether this engine version serves a metrics endpoint
	// worth scraping.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Port is the container port serving metrics. Zero means DefaultMetricsExportPort.
	Port int `json:"port,omitempty" yaml:"port,omitempty"`

	// Path is the HTTP path serving metrics. Empty means DefaultMetricsExportPath.
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
}

// PlaygroundCapability declares whether an engine version can back the console's
// Playground, and which interaction surfaces it supports.
type PlaygroundCapability struct {
	// Enabled reports whether the Playground tab should be offered at all.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Modes lists the interaction surfaces this engine version can serve, from
	// KnownPlaygroundModes. Empty means "the engine does not narrow it down":
	// consumers then fall back to inferring the surface from the endpoint's
	// model task, which is what the console did before this protocol existed.
	Modes []string `json:"modes,omitempty" yaml:"modes,omitempty"`
}

// EngineCapabilities declares what an engine version can do, so that Neutree
// stops inferring behaviour from the engine's name or its model tasks.
//
// Every field is a pointer, and nil means "this engine version did not declare
// the capability". Consumers must then fall back to the pre-protocol behaviour
// rather than to the zero value -- an engine registered by an older Neutree, or
// a package built before this protocol shipped, carries no declaration at all
// and must keep working exactly as it did. Resolve* on EngineVersion is the only
// supported way to read a capability; it applies that fallback for you.
type EngineCapabilities struct {
	// MetricsExport declares Prometheus metrics support. Nil means undeclared:
	// treated as enabled on the legacy port/path, matching the old behaviour of
	// scraping every inference pod.
	MetricsExport *MetricsExportCapability `json:"metrics_export,omitempty" yaml:"metrics_export,omitempty"`

	// Playground declares console Playground support. Nil means undeclared:
	// treated as enabled with no mode restriction, matching the old behaviour of
	// always showing the tab.
	Playground *PlaygroundCapability `json:"playground,omitempty" yaml:"playground,omitempty"`
}

// Validate checks a capability declaration at ingestion time (engine
// registration, package import). It rejects values no consumer can act on,
// rather than letting them through to be silently ignored later.
func (c *EngineCapabilities) Validate() error {
	if c == nil {
		return nil
	}

	if m := c.MetricsExport; m != nil {
		if m.Port < 0 || m.Port > 65535 {
			return fmt.Errorf("metrics_export.port %d is out of range (1-65535)", m.Port)
		}

		if m.Path != "" && !strings.HasPrefix(m.Path, "/") {
			return fmt.Errorf("metrics_export.path %q must start with %q", m.Path, "/")
		}
	}

	if p := c.Playground; p != nil {
		for _, mode := range p.Modes {
			if !IsKnownPlaygroundMode(mode) {
				return fmt.Errorf("playground.modes contains unknown mode %q, accepted values are %v",
					mode, KnownPlaygroundModes())
			}
		}
	}

	return nil
}

// ResolvedMetricsExport is a MetricsExportCapability with defaults applied, as
// returned by EngineVersion.ResolveMetricsExport. Consumers read this, never the
// raw declaration, so the undeclared-means-legacy rule lives in exactly one place.
type ResolvedMetricsExport struct {
	Enabled bool
	Port    int
	Path    string
}

// ResolvedPlayground is a PlaygroundCapability with defaults applied, as returned
// by EngineVersion.ResolvePlayground.
type ResolvedPlayground struct {
	Enabled bool

	// Modes is nil when the engine version did not narrow the interaction
	// surface down; the consumer should then infer it from the endpoint's model
	// task, as the console did before this protocol existed.
	Modes []string
}

// ResolveMetricsExport returns the effective metrics-export configuration for
// this engine version, applying the undeclared-means-legacy fallback.
func (ev *EngineVersion) ResolveMetricsExport() ResolvedMetricsExport {
	resolved := ResolvedMetricsExport{
		Enabled: true,
		Port:    DefaultMetricsExportPort,
		Path:    DefaultMetricsExportPath,
	}

	if ev == nil || ev.Capabilities == nil || ev.Capabilities.MetricsExport == nil {
		return resolved
	}

	declared := ev.Capabilities.MetricsExport
	resolved.Enabled = declared.Enabled

	if declared.Port != 0 {
		resolved.Port = declared.Port
	}

	if declared.Path != "" {
		resolved.Path = declared.Path
	}

	return resolved
}

// ResolvePlayground returns the effective Playground configuration for this
// engine version, applying the undeclared-means-legacy fallback.
func (ev *EngineVersion) ResolvePlayground() ResolvedPlayground {
	if ev == nil || ev.Capabilities == nil || ev.Capabilities.Playground == nil {
		return ResolvedPlayground{Enabled: true}
	}

	declared := ev.Capabilities.Playground

	return ResolvedPlayground{
		Enabled: declared.Enabled,
		Modes:   declared.Modes,
	}
}

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

	// Capabilities declares what this engine version can do beyond serving the
	// tasks in SupportedTasks -- whether it exports metrics, whether it can back
	// the console Playground, and so on. Capabilities are declared per version
	// because they change across releases of the same engine.
	//
	// Nil, and any nil field within, means "undeclared" and must be read through
	// the Resolve* helpers, which fall back to the pre-protocol behaviour. Never
	// read the raw struct: its zero value says "disabled", which would silently
	// break every engine registered before this protocol existed.
	//
	// Example:
	//  Capabilities: &EngineCapabilities{
	//      MetricsExport: &MetricsExportCapability{Enabled: true, Port: 8000},
	//      Playground:    &PlaygroundCapability{Enabled: true, Modes: []string{"chat"}},
	//  }
	Capabilities *EngineCapabilities `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
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

// SSHImageKeyPrefix is the key prefix used to select SSH-compatible engine images
// in the Images map (e.g., "ssh_nvidia_gpu" as the SSH-specific variant of "nvidia_gpu").
// It defines a naming convention only and does not prescribe specific image contents.
const SSHImageKeyPrefix = "ssh_"

// K8sImageKeyPrefix is the key prefix used to select K8s-only engine images
// in the Images map (e.g., "k8s_nvidia_gpu" for an upstream-native image that
// only works on Kubernetes). The K8s orchestrator looks up k8s_<accel> first,
// then falls back to the plain <accel> key. SSH/Ray orchestrators never consult
// k8s_ keys, keeping the two runtime paths isolated.
const K8sImageKeyPrefix = "k8s_"

// GetImageForAccelerator returns the image information for a specific accelerator type
// If the accelerator type is not found, it returns nil
func (ev *EngineVersion) GetImageForAccelerator(acceleratorType string) *EngineImage {
	if ev.Images == nil {
		return nil
	}

	return ev.Images[acceleratorType]
}

// GetImageForSSHAccelerator returns the engine image for SSH clusters.
// It first looks for an SSH-specific image (e.g., "ssh_nvidia_gpu"), then
// falls back to the generic accelerator key (e.g., "nvidia_gpu").
// This allows SSH-compatible images to coexist with K8s-only images in the same
// EngineVersion registration.
func (ev *EngineVersion) GetImageForSSHAccelerator(acceleratorType string) *EngineImage {
	if img := ev.GetImageForAccelerator(SSHImageKeyPrefix + acceleratorType); img != nil {
		return img
	}

	return ev.GetImageForAccelerator(acceleratorType)
}

// GetImageForK8sAccelerator returns the engine image for K8s clusters.
// It first looks for a K8s-specific image (e.g., "k8s_nvidia_gpu"), then
// falls back to the generic accelerator key (e.g., "nvidia_gpu").
// This allows users to register upstream-native images for K8s without
// affecting SSH/Ray deployments, which never consult the k8s_ prefix.
func (ev *EngineVersion) GetImageForK8sAccelerator(acceleratorType string) *EngineImage {
	if img := ev.GetImageForAccelerator(K8sImageKeyPrefix + acceleratorType); img != nil {
		return img
	}

	return ev.GetImageForAccelerator(acceleratorType)
}

// GetSupportedAccelerators returns a list of supported accelerator types.
// The list is derived from the keys of the Images map, excluding prefixed
// keys ("ssh_<accel>" and "k8s_<accel>") which are internal variants, not
// distinct accelerator types.
func (ev *EngineVersion) GetSupportedAccelerators() []string {
	if ev.Images == nil {
		return []string{}
	}

	accelerators := make([]string, 0, len(ev.Images))

	for acceleratorType := range ev.Images {
		if strings.HasPrefix(acceleratorType, SSHImageKeyPrefix) || strings.HasPrefix(acceleratorType, K8sImageKeyPrefix) {
			continue
		}

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
