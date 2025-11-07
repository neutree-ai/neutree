package engine

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// EngineVersionPackage represents the complete structure of an engine version package
type EngineVersionPackage struct {
	// Metadata contains information about the engine version package
	Metadata *PackageMetadata `json:"metadata" yaml:"metadata"`

	// Images contains the list of container images for different accelerators
	Images []*ImageSpec `json:"images" yaml:"images"`

	// EngineVersion contains the engine version definition
	EngineVersion *v1.EngineVersion `json:"engine_version" yaml:"engine_version"`
}

// PackageMetadata contains metadata about the engine version package
type PackageMetadata struct {
	// Name of the engine (e.g., "vllm", "llama-cpp")
	EngineName string `json:"engine_name" yaml:"engine_name"`

	// Version of the engine (e.g., "v0.5.0", "v1.0.0")
	Version string `json:"version" yaml:"version"`

	// Description provides details about this engine version
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Author of the package
	Author string `json:"author,omitempty" yaml:"author,omitempty"`

	// CreatedAt timestamp
	CreatedAt string `json:"created_at,omitempty" yaml:"created_at,omitempty"`

	// PackageVersion is the version of the package format itself
	PackageVersion string `json:"package_version" yaml:"package_version"`

	// Tags for categorizing the package
	Tags []string `json:"tags,omitempty" yaml:"tags,omitempty"`

	// SupportTasks lists the tasks supported by this engine
	SupportTasks []string `json:"support_tasks,omitempty" yaml:"support_tasks,omitempty"`
}

// ImageSpec describes a container image for a specific accelerator
type ImageSpec struct {
	// Accelerator type (e.g., "nvidia-gpu", "amd-gpu", "cpu")
	Accelerator string `json:"accelerator" yaml:"accelerator"`

	// ImageName is the full image reference without tag
	// Example: "neutree/vllm-cuda"
	ImageName string `json:"image_name" yaml:"image_name"`

	// Tag is the image tag
	Tag string `json:"tag" yaml:"tag"`

	// ImageFile is the path to the image tarball in the package
	// Example: "images/vllm-cuda-v0.5.0.tar"
	ImageFile string `json:"image_file" yaml:"image_file"`

	// Platform specifies the platform (e.g., "linux/amd64", "linux/arm64")
	Platform string `json:"platform,omitempty" yaml:"platform,omitempty"`

	// Size is the size of the image in bytes
	Size int64 `json:"size,omitempty" yaml:"size,omitempty"`

	// Digest is the image digest
	Digest string `json:"digest,omitempty" yaml:"digest,omitempty"`
}

// PackageManifest is the root manifest file in the engine version package
type PackageManifest struct {
	// ManifestVersion is the version of the manifest format
	ManifestVersion string `json:"manifest_version" yaml:"manifest_version"`

	// Package contains the engine version package details
	Package *EngineVersionPackage `json:"package" yaml:"package"`
}

// ImportOptions contains options for importing an engine version package
type ImportOptions struct {
	// PackagePath is the path to the engine version package file (.tar.gz or .zip)
	PackagePath string

	// ImageRegistry is the target image registry to push images to
	ImageRegistry string

	// Workspace is the workspace to import the engine to
	Workspace string

	// SkipImagePush skips pushing images to the registry
	SkipImagePush bool

	// Force forces the import even if the engine version already exists
	Force bool

	// ExtractPath is the path to extract the package to (temporary directory)
	ExtractPath string
}

// ImportResult contains the result of importing an engine version package
type ImportResult struct {
	// EngineName is the name of the engine
	EngineName string

	// Version is the version of the engine
	Version string

	// ImagesImported is the list of images that were imported
	ImagesImported []string

	// EngineUpdated indicates whether the engine was updated
	EngineUpdated bool

	// Errors contains any errors that occurred during import
	Errors []error
}
