package compose

import (
    "context"
    "fmt"
    "github.com/compose-spec/compose-go/cli"
    "github.com/compose-spec/compose-go/loader"
    "github.com/docker/docker/api/types/container"
    "github.com/docker/docker/api/types/network"
    "github.com/docker/docker/client"
    // types is used via subpackages
    imageTypes "github.com/docker/docker/api/types/image"
    "github.com/docker/docker/errdefs"
    "github.com/pkg/errors"
)

// Runner runs a docker compose project using the Docker Engine API
type SDKRunner interface {
    Up(ctx context.Context, composeFile string, projectName string) error
}

type defaultRunner struct{}

func NewSDKRunner() SDKRunner {
    return &defaultRunner{}
}

// Up brings up the project: for now it pulls images and creates/starts containers for each service
// NOTE: This is a minimal implementation and does not cover all Compose features.
func (r *defaultRunner) Up(ctx context.Context, composeFile string, projectName string) error {
    // parse project
    opts, err := cli.NewProjectOptions([]string{composeFile}, cli.WithLoadOptions(func(o *loader.Options) { o.SkipInterpolation = true }))
    if err != nil {
        return errors.Wrap(err, "create compose project options")
    }

    project, err := cli.ProjectFromOptions(opts)
    if err != nil {
        return errors.Wrap(err, "load compose project")
    }

    // create docker client
    c, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
    if err != nil {
        return errors.Wrap(err, "create docker client")
    }

    // create network for project
    netName := projectName
    if netName == "" {
        netName = project.Name
    }
    _, err = c.NetworkCreate(ctx, netName, network.CreateOptions{})
    if err != nil {
        // if network exists, continue
        if !errdefs.IsConflict(err) {
            // older API may return error string; try to ignore exists condition
        }
    }

    for _, service := range project.Services {
        // pull image
    _, err := c.ImagePull(ctx, service.Image, imageTypes.PullOptions{})
        if err != nil {
            return errors.Wrapf(err, "image pull failed: %s", service.Image)
        }

        // create container
        // convert environment map to KEY=VALUE list
        envs := []string{}
        for k, v := range service.Environment {
            if v == nil {
                envs = append(envs, k)
            } else {
                envs = append(envs, fmt.Sprintf("%s=%s", k, *v))
            }
        }

        cfg := &container.Config{ Image: service.Image, Env: envs }
        hostCfg := &container.HostConfig{}
        if len(service.Ports) > 0 {
            // not mapping ports thoroughly here; future improvement
        }
        // TODO: map compose volumes -> docker mount options

        resp, err := c.ContainerCreate(ctx, cfg, hostCfg, &network.NetworkingConfig{}, nil, service.Name)
        if err != nil {
            return errors.Wrapf(err, "creating container for %s failed", service.Name)
        }

        if err := c.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
            return errors.Wrapf(err, "starting container %s failed", service.Name)
        }
    }

    return nil
}
