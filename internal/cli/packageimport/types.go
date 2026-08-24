package packageimport

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

type PackageManifest struct {
	// ManifestVersion is the version of the manifest format
	ManifestVersion string `json:"manifest_version" yaml:"manifest_version"`

	// Metadata contains information about the package
	Metadata *PackageMetadata `json:"metadata" yaml:"metadata"`

	// Images contains the list of container images
	Images []*ImageSpec `json:"images" yaml:"images"`

	// Engines contains the list of engines need to be imported
	Engines []*EngineMetadata `json:"engines" yaml:"engines"`

	// ClusterProfile is the exact dual-type Cluster component image matrix
	// carried by a cluster image package.
	ClusterProfile *ClusterProfile `json:"cluster_profile,omitempty" yaml:"cluster_profile,omitempty"`
}

// ClusterProfile is the package representation of one exact Cluster Profile.
// Semantic validation remains at the API boundary.
type ClusterProfile struct {
	Version    string                              `json:"version" yaml:"version"`
	Components map[string]ClusterProfileComponents `json:"components" yaml:"components"`
}

type ClusterProfileComponents struct {
	RayRuntime        ClusterImageRef `json:"ray_runtime" yaml:"ray_runtime"`
	KubernetesRuntime ClusterImageRef `json:"kubernetes_runtime" yaml:"kubernetes_runtime"`
	Router            ClusterImageRef `json:"router" yaml:"router"`
	NodeAgent         ClusterImageRef `json:"node_agent" yaml:"node_agent"`
	NodeExporter      ClusterImageRef `json:"node_exporter" yaml:"node_exporter"`
	VMAgent           ClusterImageRef `json:"vmagent" yaml:"vmagent"`
	KubeStateMetrics  ClusterImageRef `json:"kube_state_metrics" yaml:"kube_state_metrics"`
}

type ClusterImageRef struct {
	Image string `json:"image" yaml:"image"`
	Tag   string `json:"tag" yaml:"tag"`
}

// ToAPIClusterProfile converts a package payload to the internal API object.
func (profile *ClusterProfile) ToAPIClusterProfile() *v1.ClusterProfile {
	if profile == nil {
		return nil
	}

	components := make(map[string]v1.ClusterProfileComponents, len(profile.Components))
	for clusterType, value := range profile.Components {
		components[clusterType] = v1.ClusterProfileComponents{
			RayRuntime:        v1.ImageRef{Image: value.RayRuntime.Image, Tag: value.RayRuntime.Tag},
			KubernetesRuntime: v1.ImageRef{Image: value.KubernetesRuntime.Image, Tag: value.KubernetesRuntime.Tag},
			Router:            v1.ImageRef{Image: value.Router.Image, Tag: value.Router.Tag},
			NodeAgent:         v1.ImageRef{Image: value.NodeAgent.Image, Tag: value.NodeAgent.Tag},
			NodeExporter:      v1.ImageRef{Image: value.NodeExporter.Image, Tag: value.NodeExporter.Tag},
			VMAgent:           v1.ImageRef{Image: value.VMAgent.Image, Tag: value.VMAgent.Tag},
			KubeStateMetrics:  v1.ImageRef{Image: value.KubeStateMetrics.Image, Tag: value.KubeStateMetrics.Tag},
		}
	}

	return &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: profile.Version},
		Spec:       &v1.ClusterProfileSpec{Components: components},
	}
}

type EngineMetadata struct {
	// Name of the engine
	Name string `json:"name" yaml:"name"`

	EngineVersions []*v1.EngineVersion `json:"engine_versions" yaml:"engine_versions"`

	SupportedTasks []string `json:"supported_tasks,omitempty" yaml:"supported_tasks,omitempty"`
}

// PackageMetadata contains metadata about the engine version package
type PackageMetadata struct {
	// Author of the package
	Author string `json:"author,omitempty" yaml:"author,omitempty"`

	// CreatedAt timestamp
	CreatedAt string `json:"created_at,omitempty" yaml:"created_at,omitempty"`

	// Version is the version of the neutree format itself
	Version string `json:"version" yaml:"version"`

	// Tags for categorizing the package
	Tags []string `json:"tags,omitempty" yaml:"tags,omitempty"`

	// PackageURL is the URL to download the full package archive (.tar.gz)
	PackageURL string `json:"package_url,omitempty" yaml:"package_url,omitempty"`
}

// ImageSpec describes a container image for a specific accelerator
type ImageSpec struct {
	// ImageName is the full image reference without tag
	// Example: "neutree/vllm-cuda"
	ImageName string `json:"image_name" yaml:"image_name"`

	// Tag is the image tag
	Tag string `json:"tag" yaml:"tag"`

	// Platform specifies the platform (e.g., "linux/amd64", "linux/arm64")
	Platform string `json:"platform,omitempty" yaml:"platform,omitempty"`

	// Size is the size of the image in bytes
	Size int64 `json:"size,omitempty" yaml:"size,omitempty"`

	// Digest is the image digest
	Digest string `json:"digest,omitempty" yaml:"digest,omitempty"`

	// ImageFile is the path to the image file within the package
	ImageFile string `json:"image_file" yaml:"image_file"`
}

// ImportOptions contains options for importing an engine version package
type ImportOptions struct {
	// PackagePath is the path to the engine version package file (.tar.gz or .zip)
	PackagePath string

	// MirrorRegistry is the mirror registry to push images to.
	// This field is required when SkipImagePush is false (i.e., when pushing images).
	MirrorRegistry string

	// RegistryProject is the project/namespace in the registry to push images to.
	// When set, images are pushed to MirrorRegistry/RegistryProject/imageName.
	RegistryProject string

	// RegistryUser is the username for the mirror image registry
	RegistryUser string

	// RegistryPassword is the password for the mirror image registry
	RegistryPassword string

	// Workspace is the workspace to import the engine to
	Workspace string

	// SkipImagePush skips pushing images to the registry
	SkipImagePush bool

	// SkipImageLoad skips loading images from files
	SkipImageLoad bool

	// Force forces the import even if the engine version already exists
	Force bool

	// ExtractPath is the path to extract the package to (temporary directory)
	ExtractPath string
}

// ImportResult contains the result of importing an engine version package
type ImportResult struct {
	// ImagesImported is the list of images that were imported
	ImagesImported []string

	// EnginesImported is the list of engines that were imported
	EnginesImported []*EngineMetadata

	// Version is the imported package version
	Version string

	// Errors contains any errors that occurred during import
	Errors []error
}
