package util

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// BuildClusterImageRef constructs the full cluster image reference.
// imageSuffix is the accelerator-specific suffix (e.g. "rocm") from RuntimeConfig.ImageSuffix;
// pass empty string for the default (NVIDIA) variant.
//
// Examples:
//
//	BuildClusterImageRef("registry.io/neutree", "v1.0.0", "")     → "registry.io/neutree/neutree-serve:v1.0.0"
//	BuildClusterImageRef("registry.io/neutree", "v1.0.0", "rocm") → "registry.io/neutree/neutree-serve:v1.0.0-rocm"
func BuildClusterImageRef(imagePrefix, version, imageSuffix string) string {
	tag := version
	if imageSuffix != "" {
		tag = version + "-" + imageSuffix
	}

	return imagePrefix + "/" + v1.NeutreeServeImageName + ":" + tag
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

	if imagePrefix != "" {
		return imagePrefix + "/" + imageName + ":" + tag
	}

	return imageName + ":" + tag
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
