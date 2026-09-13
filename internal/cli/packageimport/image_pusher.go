package packageimport

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"
)

type imageSnapshot struct {
	ids  map[string]struct{}
	refs map[string]string
}

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

	return &ImagePusher{dockerClient: dockerClient}, nil
}

// SnapshotLocalImages captures local image IDs and tags before package import.
func (p *ImagePusher) SnapshotLocalImages(ctx context.Context) (*imageSnapshot, error) {
	images, err := p.dockerClient.ImageList(ctx, image.ListOptions{All: true})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list local images")
	}

	snapshot := &imageSnapshot{
		ids:  make(map[string]struct{}),
		refs: make(map[string]string),
	}

	for _, imageSummary := range images {
		if imageSummary.ID != "" {
			snapshot.ids[imageSummary.ID] = struct{}{}
		}

		for _, ref := range imageSummary.RepoTags {
			if ref == "" || ref == "<none>:<none>" {
				continue
			}

			snapshot.refs[ref] = imageSummary.ID
		}
	}

	return snapshot, nil
}

// CleanupImages removes newly introduced image references after all pushes succeed.
func (p *ImagePusher) CleanupImages(ctx context.Context, before *imageSnapshot, mirrorRegistry string, manifest *PackageManifest) error {
	after, err := p.SnapshotLocalImages(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to snapshot local images for cleanup")
	}

	targetRefs := make(map[string]struct{})
	sourceRefs := make(map[string]struct{})

	for _, imageSpec := range manifest.Images {
		sourceRef := fmt.Sprintf("%s:%s", imageSpec.ImageName, imageSpec.Tag)
		targetRef := p.buildTargetImage(mirrorRegistry, imageSpec)

		if isCleanupCandidate(before, after, targetRef) {
			targetRefs[targetRef] = struct{}{}
		}

		if isCleanupCandidate(before, after, sourceRef) {
			sourceRefs[sourceRef] = struct{}{}
		}
	}

	var cleanupErrors []string
	cleanupRefs := func(refs map[string]struct{}) {
		for _, ref := range sortedImageRefs(refs) {
			if _, err := p.dockerClient.ImageRemove(ctx, ref, image.RemoveOptions{}); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Sprintf("%s: %v", ref, err))
			}
		}
	}
	cleanupRefs(targetRefs)
	cleanupRefs(sourceRefs)

	if len(cleanupErrors) > 0 {
		return errors.Errorf("failed to clean local image references: %s", strings.Join(cleanupErrors, "; "))
	}

	return nil
}

func isCleanupCandidate(before, after *imageSnapshot, ref string) bool {
	if before == nil || after == nil {
		return false
	}

	if _, existed := before.refs[ref]; existed {
		return false
	}

	imageID, exists := after.refs[ref]
	if !exists || imageID == "" {
		return false
	}

	_, existed := before.ids[imageID]

	return !existed
}

func sortedImageRefs(refs map[string]struct{}) []string {
	result := make([]string, 0, len(refs))
	for ref := range refs {
		result = append(result, ref)
	}

	sort.Strings(result)

	return result
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

	if shouldAddDockerHubLibraryPrefix(imgSpec.ImageName, imageName) {
		imageName = "library/" + imageName
	}

	return fmt.Sprintf("%s/%s:%s", imagePrefix, imageName, imgSpec.Tag)
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
