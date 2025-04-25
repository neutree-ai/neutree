package cluster

import (
	"context"
	"net/url"

	"github.com/google/go-containerregistry/pkg/authn"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/pkg/errors"
)

var (
	ErrImageNotFound     = errors.New("image not found")
	ErrorRayNodeNotFound = errors.New("ray node not found")
)

type ClusterManager interface {
	UpCluster(ctx context.Context, restart bool) (string, error)
	DownCluster(ctx context.Context) error
	StartNode(ctx context.Context, nodeIP string) error
	StopNode(ctx context.Context, nodeIP string) error
	GetDesireStaticWorkersIP(ctx context.Context) []string
	GetDashboardService(ctx context.Context) (dashboard.DashboardService, error)
	GetServeEndpoint(ctx context.Context) (string, error)
	Sync(ctx context.Context) error
}

func checkClusterImage(imageService registry.ImageService, cluster *v1.Cluster, imageRegistry *v1.ImageRegistry) error {
	if imageRegistry.Status == nil || imageRegistry.Status.Phase != v1.ImageRegistryPhaseCONNECTED {
		return errors.New("image registry " + imageRegistry.Metadata.Name + " not connected")
	}

	image, err := getClusterImage(cluster, imageRegistry)
	if err != nil {
		return errors.Wrapf(err, "failed to get cluster image for cluster %s", cluster.Metadata.Name)
	}

	imageExisted, err := imageService.CheckImageExists(image, authn.FromConfig(authn.AuthConfig{
		Username:      imageRegistry.Spec.AuthConfig.Username,
		Password:      imageRegistry.Spec.AuthConfig.Password,
		Auth:          imageRegistry.Spec.AuthConfig.Auth,
		IdentityToken: imageRegistry.Spec.AuthConfig.IdentityToken,
		RegistryToken: imageRegistry.Spec.AuthConfig.IdentityToken,
	}))

	if err != nil {
		return err
	}

	if !imageExisted {
		return errors.Wrap(ErrImageNotFound, "image "+image+" not found")
	}

	return nil
}

func getClusterImage(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry) (string, error) {
	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	return registryURL.Host + "/" + imageRegistry.Spec.Repository + "/neutree-serve:" + cluster.Spec.Version, nil
}
