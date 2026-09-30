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
		originalImage := buildImageRef(imgSpec)
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
	name, tag := imageNameAndTag(imgSpec)

	// Remove any existing registry from the image name
	imageName := extractImageNameWithoutRegistry(name)

	if shouldAddDockerHubLibraryPrefix(name, imageName) {
		imageName = "library/" + imageName
	}

	return fmt.Sprintf("%s/%s:%s", imagePrefix, imageName, tag)
}

// buildImageRef returns the full reference of a manifest image — the reference
// the package archive holds it under — by joining the manifest's two fields.
func buildImageRef(imgSpec *ImageSpec) string {
	if imgSpec.Tag == "" {
		return imgSpec.ImageName
	}

	return imgSpec.ImageName + ":" + imgSpec.Tag
}

// imageNameAndTag splits a manifest image into its name and tag.
//
// The two fields are joined before splitting, because they are not necessarily
// split where the reference is: builds before the tag separator was fixed
// recorded "harbor.example.cn:5443/team/img:v1" as image_name
// "harbor.example.cn" plus tag "5443/team/img:v1". Joining restores the
// reference the archive holds, and re-splitting then yields the name and tag
// the mirror push needs.
func imageNameAndTag(imgSpec *ImageSpec) (name, tag string) {
	return splitImageTag(buildImageRef(imgSpec))
}

// splitImageTag cuts a reference at its tag. The separator is the last ":" and
// only when it follows the last "/", so the port of a registry host stays part
// of the name and a reference without a tag keeps its name whole.
func splitImageTag(ref string) (name, tag string) {
	lastSlash := strings.LastIndex(ref, "/")
	lastColon := strings.LastIndex(ref, ":")

	if lastColon > lastSlash {
		return ref[:lastColon], ref[lastColon+1:]
	}

	return ref, ""
}

func shouldAddDockerHubLibraryPrefix(originalImageName, imageName string) bool {
	return !strings.Contains(imageName, "/") && isDockerHubImageName(originalImageName)
}

func isDockerHubImageName(imageName string) bool {
	registry, ok := imageRegistry(imageName)
	if !ok {
		return true
	}

	return registry == "docker.io" || registry == "index.docker.io" || registry == "registry-1.docker.io"
}

func imageRegistry(imageName string) (string, bool) {
	idx := strings.Index(imageName, "/")
	if idx == -1 {
		return "", false
	}

	firstPart := imageName[:idx]
	if strings.Contains(firstPart, ".") || strings.Contains(firstPart, ":") {
		return firstPart, true
	}

	return "", false
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
	if registry, ok := imageRegistry(imageName); ok {
		return strings.TrimPrefix(imageName, registry+"/")
	}

	return imageName
}
