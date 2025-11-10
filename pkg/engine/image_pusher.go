package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	apiclient "github.com/neutree-ai/neutree/pkg/client"
)

// ImagePusher handles pushing container images to registries
type ImagePusher struct {
	apiClient    *apiclient.Client
	dockerClient *client.Client
}

// NewImagePusher creates a new ImagePusher
func NewImagePusher(apiClient *apiclient.Client) (*ImagePusher, error) {
	// Create Docker client
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Docker client")
	}

	return &ImagePusher{
		apiClient:    apiClient,
		dockerClient: dockerClient,
	}, nil
}

// LoadAndPushImages loads images from tar files and pushes them to the registry
// It handles getting the image registry and pushing images
func (p *ImagePusher) LoadAndPushImages(ctx context.Context, workspace, registryName string, manifest *PackageManifest, extractedPath string) ([]string, error) {
	// Get image registry
	imageRegistry, err := p.apiClient.ImageRegistries.Get(workspace, registryName)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image registry")
	}

	klog.Infof("Using image registry: %s", imageRegistry.Metadata.Name)

	// Load and push images
	return p.loadAndPushImages(ctx, imageRegistry, manifest, extractedPath)
}

// loadAndPushImages is the internal implementation that loads and pushes images
func (p *ImagePusher) loadAndPushImages(ctx context.Context, imageRegistry *v1.ImageRegistry, manifest *PackageManifest, extractedPath string) ([]string, error) {
	var pushedImages []string
	var errs []error

	targetImagePrefix, err := util.GetImagePrefix(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image prefix")
	}

	// Initialize the Images map if it doesn't exist
	if manifest.Package.EngineVersion.Images == nil {
		manifest.Package.EngineVersion.Images = make(map[string]*v1.EngineImage)
	}

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
		targetImage := p.buildTargetImage(targetImagePrefix, imgSpec)

		// Tag the image with the target registry
		klog.Infof("Tagging image %s as %s", originalImage, targetImage)

		if err := p.tagImage(ctx, originalImage, targetImage); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to tag image"))
			continue
		}

		// Push the image to the registry
		klog.Infof("Pushing image %s to registry", targetImage)

		if err := p.pushImage(ctx, imageRegistry, targetImage); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to push image"))
			continue
		}

		pushedImages = append(pushedImages, targetImage)
		klog.Infof("Successfully pushed image: %s", targetImage)

		// Update the manifest's EngineVersion.Images with the processed image name (without old registry)
		processedImageName := p.extractImageNameWithoutRegistry(imgSpec.ImageName)
		manifest.Package.EngineVersion.Images[imgSpec.Accelerator] = &v1.EngineImage{
			ImageName: processedImageName,
			Tag:       imgSpec.Tag,
		}

		klog.V(2).Infof("Updated manifest image for accelerator %s: %s:%s", imgSpec.Accelerator, processedImageName, imgSpec.Tag)
	}

	if len(errs) > 0 {
		return pushedImages, errors.Errorf("failed to push %d images: %v", len(errs), errs)
	}

	return pushedImages, nil
}

// buildTargetImage builds the target image reference with registry and repo
func (p *ImagePusher) buildTargetImage(imagePrefix string, imgSpec *ImageSpec) string {
	// Remove any existing registry from the image name
	imageName := p.extractImageNameWithoutRegistry(imgSpec.ImageName)
	return fmt.Sprintf("%s/%s:%s", imagePrefix, imageName, imgSpec.Tag)
}

// extractImageNameWithoutRegistry removes any existing registry prefix from the image name
func (p *ImagePusher) extractImageNameWithoutRegistry(imageName string) string {
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
func (p *ImagePusher) pushImage(ctx context.Context, imageRegistry *v1.ImageRegistry, imageName string) error {
	// Create push options with auth
	pushOptions := image.PushOptions{}

	// Add auth if credentials are available
	userName, password := util.GetImageRegistryAuthInfo(imageRegistry)
	if userName != "" && password != "" {
		registryHost, err := util.GetImageRegistryHost(imageRegistry)
		if err != nil {
			return errors.Wrap(err, "failed to get registry host")
		}

		authConfig := registry.AuthConfig{
			Username:      userName,
			Password:      password,
			ServerAddress: registryHost,
		}

		authConfigBytes, err := json.Marshal(authConfig)
		if err != nil {
			return errors.Wrap(err, "failed to marshal auth config")
		}

		pushOptions.RegistryAuth = base64.URLEncoding.EncodeToString(authConfigBytes)
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
