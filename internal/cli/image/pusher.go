// Package image implements pushing arbitrary container images into a
// Neutree-managed image registry, so that engine `image` overrides (e.g. the
// flex engine) can reference workload images the platform can actually pull.
package image

import (
	"context"
	"io"
	"os"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/pkg/errors"
)

// dockerAPI is the subset of the Docker client used by Pusher.
type dockerAPI interface {
	ImageLoad(ctx context.Context, input io.Reader, opts ...client.ImageLoadOption) (image.LoadResponse, error)
	ImageTag(ctx context.Context, source, target string) error
	ImagePush(ctx context.Context, ref string, options image.PushOptions) (io.ReadCloser, error)
}

// Pusher tags and pushes images that are present in the local Docker daemon.
type Pusher struct {
	docker dockerAPI
	out    io.Writer
}

// NewPusher creates a Pusher backed by the Docker daemon configured in the
// environment (DOCKER_HOST and friends). Progress is streamed to out.
func NewPusher(out io.Writer) (*Pusher, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Docker client")
	}

	if out == nil {
		out = io.Discard
	}

	return &Pusher{docker: dockerClient, out: out}, nil
}

// LoadArchive loads a `docker save` tar archive into the local daemon.
func (p *Pusher) LoadArchive(ctx context.Context, archivePath string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return errors.Wrapf(err, "failed to open image archive: %s", archivePath)
	}
	defer file.Close()

	resp, err := p.docker.ImageLoad(ctx, file)
	if err != nil {
		return errors.Wrapf(err, "docker load failed for %s", archivePath)
	}
	defer resp.Body.Close()

	return errors.Wrapf(p.display(resp.Body), "failed to display load progress for %s", archivePath)
}

// TagAndPush tags source as target and pushes target using registryAuth, the
// base64-encoded Docker auth blob produced by EncodeRegistryAuth.
func (p *Pusher) TagAndPush(ctx context.Context, source, target, registryAuth string) error {
	if err := p.docker.ImageTag(ctx, source, target); err != nil {
		return errors.Wrapf(err,
			"failed to tag %s as %s (is the image present in the local Docker daemon? try `docker pull %s` or --archive)",
			source, target, source)
	}

	resp, err := p.docker.ImagePush(ctx, target, image.PushOptions{RegistryAuth: registryAuth})
	if err != nil {
		return errors.Wrapf(err, "docker push failed for %s", target)
	}
	defer resp.Close()

	return errors.Wrapf(p.display(resp), "failed to display push progress for %s", target)
}

func (p *Pusher) display(body io.Reader) error {
	return jsonmessage.DisplayJSONMessagesStream(body, p.out, 0, false, nil)
}
