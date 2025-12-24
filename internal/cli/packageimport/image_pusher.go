package packageimport

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"
)

// ImagePusher handles pushing container images to registries
type ImagePusher struct {
	dockerClient *client.Client
}

// NewImagePusher creates a new ImagePusher
func NewImagePusher() (*ImagePusher, error) {
	// Create Docker client
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Docker client")
	}

	return &ImagePusher{
		dockerClient: dockerClient,
	}, nil
}

func (p *ImagePusher) LoadImages(ctx context.Context, manifest *PackageManifest, extractedPath string) error {
	klog.Infof("Loading images from extracted path: %s", extractedPath)

	// Load images
	return p.loadImages(ctx, manifest, extractedPath)
}

// loadImages is the internal implementation that loads images
func (p *ImagePusher) loadImages(ctx context.Context, manifest *PackageManifest, extractedPath string) error {
	alreadyLoadedImageFile := make(map[string]bool)

	for _, imgSpec := range manifest.Images {
		imagePath := fmt.Sprintf("%s/%s", extractedPath, imgSpec.ImageFile)

		// Skip loading if already loaded
		if alreadyLoadedImageFile[imgSpec.ImageFile] {
			klog.Infof("Image file %s already loaded, skipping load", imgSpec.ImageFile)
			continue
		}

		// Load the image
		klog.Infof("Loading image from %s", imagePath)

		if err := p.loadImage(ctx, imagePath); err != nil {
			return errors.Wrapf(err, "failed to load image: %s", imagePath)
		}

		alreadyLoadedImageFile[imgSpec.ImageFile] = true
	}

	return nil
}

func (p *ImagePusher) PushImagesToMirrorRegistry(ctx context.Context,
	mirrorRegistry string, registryAuth string, manifest *PackageManifest) ([]string, error) {
	klog.Infof("Pushing images to mirror registry: %s", mirrorRegistry)

	// Load and push images
	return p.pushImages(ctx, mirrorRegistry, registryAuth, manifest)
}

// loadAndPushImages is the internal implementation that loads and pushes images
func (p *ImagePusher) pushImages(ctx context.Context, mirrorRegistry string, registryAuth string,
	manifest *PackageManifest) ([]string, error) {
	var pushedImages []string
	var errs []error

	for _, imgSpec := range manifest.Images {
		// Build the original and target image references
		originalImage := fmt.Sprintf("%s:%s", imgSpec.ImageName, imgSpec.Tag)
		targetImage := p.buildTargetImage(mirrorRegistry, imgSpec)

		// Tag the image with the target registry
		klog.Infof("Tagging image %s as %s", originalImage, targetImage)

		if err := p.tagImage(ctx, originalImage, targetImage); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to tag image"))
			continue
		}

		// Push the image to the registry
		klog.Infof("Pushing image %s to registry", targetImage)

		if err := p.pushImage(ctx, targetImage, registryAuth); err != nil {
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

// buildTargetImage builds the target image reference with registry and repo
func (p *ImagePusher) buildTargetImage(imagePrefix string, imgSpec *ImageSpec) string {
	// Remove any existing registry from the image name
	imageName := extractImageNameWithoutRegistry(imgSpec.ImageName)

	// If the image name doesn't contain a slash, it's a Docker Hub library image
	// We need to add the "library" prefix to maintain the correct path structure
	if !strings.Contains(imageName, "/") {
		imageName = "library/" + imageName
	}

	return fmt.Sprintf("%s/%s:%s", imagePrefix, imageName, imgSpec.Tag)
}

// loadImage loads a Docker image from a tar file
func (p *ImagePusher) loadImage(ctx context.Context, imagePath string) error {
	// Open the tar file
	file, err := os.Open(imagePath)
	if err != nil {
		return errors.Wrapf(err, "failed to open image file: %s", imagePath)
	}
	defer file.Close()

	// Load the image
	resp, err := p.dockerClient.ImageLoad(ctx, file)
	if err != nil {
		return errors.Wrapf(err, "docker load failed for %s", imagePath)
	}
	defer resp.Body.Close()

	// Display progress using Docker's jsonmessage package (similar to Docker CLI)
	// Use a simple writer that logs to klog
	out := &klogWriter{prefix: "Docker load"}
	if err := jsonmessage.DisplayJSONMessagesStream(resp.Body, out, 0, false, nil); err != nil {
		klog.Warningf("Failed to display docker load output: %v", err)
	}

	return nil
}

// tagImage tags a Docker image
func (p *ImagePusher) tagImage(ctx context.Context, sourceImage, targetImage string) error {
	err := p.dockerClient.ImageTag(ctx, sourceImage, targetImage)
	if err != nil {
		return errors.Wrapf(err, "docker tag failed for %s -> %s", sourceImage, targetImage)
	}

	return nil
}

// pushImage pushes a Docker image to a registry
func (p *ImagePusher) pushImage(ctx context.Context, imageName string, registryAuth string) error {
	// Create push options with auth
	pushOptions := image.PushOptions{
		RegistryAuth: registryAuth,
	}

	// Push the image
	resp, err := p.dockerClient.ImagePush(ctx, imageName, pushOptions)
	if err != nil {
		return errors.Wrapf(err, "docker push failed for %s", imageName)
	}
	defer resp.Close()

	// Display progress using Docker's jsonmessage package (similar to Docker CLI)
	// Reference: https://github.com/docker/cli/blob/master/cli/command/image/push.go
	out := &klogWriter{prefix: "Docker push"}
	if err := jsonmessage.DisplayJSONMessagesStream(resp, out, 0, false, nil); err != nil {
		return errors.Wrapf(err, "failed to display push progress for %s", imageName)
	}

	return nil
}

// klogWriter implements io.Writer to output Docker progress to klog
type klogWriter struct {
	prefix string
}

func (w *klogWriter) Write(p []byte) (int, error) {
	// Remove trailing newline for cleaner klog output
	msg := strings.TrimSuffix(string(p), "\n")
	if msg != "" {
		klog.Infof("%s: %s", w.prefix, msg)
	}

	return len(p), nil
}

// extractImageNameWithoutRegistry removes any existing registry prefix from the image name
func extractImageNameWithoutRegistry(imageName string) string {
	// Check if there's a registry prefix (contains / before any other structure)
	if idx := strings.Index(imageName, "/"); idx != -1 {
		// Check if this is a registry prefix (contains . or :)
		firstPart := imageName[:idx]
		if strings.Contains(firstPart, ".") || strings.Contains(firstPart, ":") {
			// This is a registry, remove it
			return imageName[idx+1:]
		}
	}
	// No registry prefix found, return as-is
	return imageName
}
