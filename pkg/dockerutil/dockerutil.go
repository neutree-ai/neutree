package dockerutil

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/compose-spec/compose-go/cli"
	"github.com/compose-spec/compose-go/loader"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// PullImages pulls multiple images using local Docker daemon
func PullImages(ctx context.Context, images []string) error {
	if len(images) == 0 {
		return nil
	}

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create docker client: %w", err)
	}

	for _, img := range images {
		out, err := dockerClient.ImagePull(ctx, img, imagetypes.PullOptions{})
		if err != nil {
			return fmt.Errorf("failed to pull image %s: %w", img, err)
		}
		// drain output to show any progress; ignore errors
		io.Copy(os.Stdout, out)
		out.Close()
	}

	return nil
}

// PullImagesFromCompose loads a docker-compose file and pulls all service images
func PullImagesFromCompose(ctx context.Context, composeFile string) ([]string, error) {
	opts, err := cli.NewProjectOptions([]string{composeFile}, cli.WithLoadOptions(func(o *loader.Options) {
		o.SkipInterpolation = true
	}))
	if err != nil {
		return nil, fmt.Errorf("load compose options: %w", err)
	}

	project, err := cli.ProjectFromOptions(opts)
	if err != nil {
		return nil, fmt.Errorf("parse compose project: %w", err)
	}

	var images []string
	for _, svc := range project.Services {
		if svc.Image != "" {
			images = append(images, svc.Image)
		}
	}

	if err := PullImages(ctx, images); err != nil {
		return images, err
	}

	return images, nil
}
