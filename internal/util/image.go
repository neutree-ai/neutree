package util

import (
	"fmt"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// BuildClusterImageRef constructs the legacy SSH Ray runtime image reference.
// imageSuffix is the accelerator-specific suffix from RuntimeConfig.ImageSuffix.
func BuildClusterImageRef(imagePrefix, version, imageSuffix string) string {
	tag := version
	if imageSuffix != "" {
		tag += "-" + imageSuffix
	}

	return RewriteImageRef(imagePrefix, v1.NeutreeServeImageName+":"+tag)
}

// BuildProfileImageRef constructs a pull-side reference from an exact
// ClusterProfile image and tag. Profile validation owns the normal invariant;
// this boundary keeps runtime failures clear for incomplete test fixtures or
// unexpected persisted data.
func BuildProfileImageRef(imagePrefix, component string, ref v1.ImageRef) (string, error) {
	image := strings.TrimSpace(ref.Image)
	if image == "" || image != ref.Image {
		return "", fmt.Errorf("%s image is required", component)
	}

	tag := strings.TrimSpace(ref.Tag)
	if tag == "" || tag != ref.Tag {
		return "", fmt.Errorf("%s tag is required", component)
	}

	return RewriteImageRef(imagePrefix, image+":"+tag), nil
}

// BuildEngineImageRef constructs the full engine image reference from an EngineImage.
// Returns empty string if engineImage is nil or has no ImageName.
//
// Examples:
//
//	BuildEngineImageRef("registry.io/neutree", &EngineImage{ImageName: "neutree/vllm", Tag: "v0.11.2"})
//	→ "registry.io/neutree/neutree/vllm:v0.11.2"
func BuildEngineImageRef(imagePrefix string, engineImage *v1.EngineImage) string {
	if engineImage == nil {
		return ""
	}

	imageName, tag := engineImage.GetFullImagePath()
	if imageName == "" {
		return ""
	}

	return RewriteImageRef(imagePrefix, imageName+":"+tag)
}

// RewriteImageRef rewrites image into imagePrefix while preserving the image
// repository path and removing any source registry host. Docker Hub prefixes
// leave image references unchanged.
//
// This is the pull-side rule: a reference that is already pullable from Docker
// Hub needs no prefix. Use RelocateImageRef when choosing a push destination.
func RewriteImageRef(imagePrefix, image string) string {
	if IsDockerHubImagePrefix(imagePrefix) {
		return image
	}

	return RelocateImageRef(imagePrefix, image)
}

// RelocateImageRef rewrites image into imagePrefix for a push, preserving the
// repository path and removing any source registry host.
//
// Unlike RewriteImageRef it applies the prefix even when the prefix is Docker
// Hub: a push must land in the registry the caller named. Skipping the prefix
// there would push the image back to whatever registry the source reference
// points at — a different host than the one whose credentials are in play.
func RelocateImageRef(imagePrefix, image string) string {
	if image == "" {
		return ""
	}

	imagePrefix = strings.TrimRight(strings.TrimSpace(imagePrefix), "/")
	if imagePrefix == "" || strings.HasPrefix(image, imagePrefix+"/") {
		return image
	}

	return imagePrefix + "/" + stripSourceImageRegistry(image)
}

// IsDockerHubImagePrefix reports whether imagePrefix targets Docker Hub.
func IsDockerHubImagePrefix(imagePrefix string) bool {
	host := strings.SplitN(strings.Trim(strings.TrimSpace(imagePrefix), "/"), "/", 2)[0]

	switch strings.ToLower(host) {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return true
	default:
		return false
	}
}

func stripSourceImageRegistry(image string) string {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) < 2 {
		return image
	}

	if isSourceImageRegistry(parts[0]) {
		return parts[1]
	}

	return image
}

func isSourceImageRegistry(segment string) bool {
	return segment == "localhost" || strings.Contains(segment, ".") || strings.Contains(segment, ":")
}

// ResolveEngineImage finds the engine image for a given engine version and accelerator type,
// and returns the full image reference string.
// If acceleratorType is empty, defaults to "cpu".
// Returns empty string (no error) if the engine version has no image for the accelerator type.
func ResolveEngineImage(engineVersion *v1.EngineVersion, acceleratorType, imagePrefix string) (string, error) {
	if engineVersion == nil {
		return "", fmt.Errorf("engine version is nil")
	}

	if acceleratorType == "" {
		acceleratorType = "cpu"
	}

	engineImage := engineVersion.GetImageForAccelerator(acceleratorType)
	if engineImage == nil {
		return "", nil
	}

	return BuildEngineImageRef(imagePrefix, engineImage), nil
}
