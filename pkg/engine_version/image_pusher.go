package engine_version

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
)

// ImagePusher handles pushing container images to registries
type ImagePusher struct{}

// NewImagePusher creates a new ImagePusher
func NewImagePusher() *ImagePusher {
	return &ImagePusher{}
}

// LoadAndPushImages loads images from tar files and pushes them to the registry
func (p *ImagePusher) LoadAndPushImages(ctx context.Context, manifest *PackageManifest, extractedPath string, registry string, imagePrefix string) ([]string, error) {
	var pushedImages []string
	var errs []error

	for _, imgSpec := range manifest.Package.Images {
		imagePath := fmt.Sprintf("%s/%s", extractedPath, imgSpec.ImageFile)

		// Load the image
		klog.Infof("Loading image from %s", imagePath)

		if err := p.loadImage(ctx, imagePath); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to load image: %s", imagePath))
			continue
		}

		// Build the original and target image references
		originalImage := fmt.Sprintf("%s:%s", imgSpec.ImageName, imgSpec.Tag)
		targetImage := p.buildTargetImage(registry, imagePrefix, imgSpec)

		// Tag the image with the target registry
		klog.Infof("Tagging image %s as %s", originalImage, targetImage)

		if err := p.tagImage(ctx, originalImage, targetImage); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to tag image"))
			continue
		}

		// Push the image to the registry
		klog.Infof("Pushing image %s to registry", targetImage)

		if err := p.pushImage(ctx, targetImage); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to push image"))
			continue
		}

		pushedImages = append(pushedImages, targetImage)
		klog.Infof("Successfully pushed image: %s", targetImage)
	}

	if len(errs) > 0 {
		return pushedImages, errors.Errorf("failed to push %d images: %v", len(errs), errs)
	}

	return pushedImages, nil
}

// buildTargetImage builds the target image reference with registry and prefix
func (p *ImagePusher) buildTargetImage(registry, prefix string, imgSpec *ImageSpec) string {
	// Remove any existing registry from the image name
	imageName := imgSpec.ImageName
	if idx := strings.Index(imageName, "/"); idx != -1 {
		// Check if this is a registry prefix (contains . or :)
		firstPart := imageName[:idx]
		if strings.Contains(firstPart, ".") || strings.Contains(firstPart, ":") {
			imageName = imageName[idx+1:]
		}
	}

	// Build the target image reference
	if prefix != "" {
		return fmt.Sprintf("%s/%s/%s:%s", registry, prefix, imageName, imgSpec.Tag)
	}

	return fmt.Sprintf("%s/%s:%s", registry, imageName, imgSpec.Tag)
}

// loadImage loads a Docker image from a tar file
func (p *ImagePusher) loadImage(ctx context.Context, imagePath string) error {
	cmd := exec.CommandContext(ctx, "docker", "load", "-i", imagePath)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "docker load failed: %s", string(output))
	}

	return nil
}

// tagImage tags a Docker image
func (p *ImagePusher) tagImage(ctx context.Context, sourceImage, targetImage string) error {
	cmd := exec.CommandContext(ctx, "docker", "tag", sourceImage, targetImage)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "docker tag failed: %s", string(output))
	}

	return nil
}

// pushImage pushes a Docker image to a registry
func (p *ImagePusher) pushImage(ctx context.Context, image string) error {
	cmd := exec.CommandContext(ctx, "docker", "push", image)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "docker push failed: %s", string(output))
	}

	return nil
}
