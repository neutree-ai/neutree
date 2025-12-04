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

	// ImageRegistry is the target image registry to push images to
	ImageRegistry string

	// MirrorRegistry is an optional mirror registry to push images to
	MirrorRegistry string

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
