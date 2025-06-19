package cluster

import (
	"context"
	"fmt"
	"net/url"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/registry"
)

const (
	NvdiaAcceleratorType      = "gpu"
	Ascend310PAcceleratorType = "ascend310p"
	Ascend910BAcceleratorType = "ascend910b"
)

var acceleratorImageTagSuffix = map[string]string{
	"cpu":                     "",
	NvdiaAcceleratorType:      "",
	Ascend310PAcceleratorType: "npu-ascend310p",
	Ascend910BAcceleratorType: "npu-ascend910b",
}

var (
	ErrImageNotFound     = errors.New("image not found")
	ErrorRayNodeNotFound = errors.New("ray node not found")
)

type ClusterManager interface {
	ConnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint) error
	DisconnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint) error

	UpCluster(ctx context.Context, restart bool) (string, error)
	DownCluster(ctx context.Context) error
	StartNode(ctx context.Context, nodeIP string) error
	StopNode(ctx context.Context, nodeIP string) error
	GetDesireStaticWorkersIP(ctx context.Context) []string
	GetDashboardService(ctx context.Context) (dashboard.DashboardService, error)
	GetServeEndpoint(ctx context.Context) (string, error)
	Sync(ctx context.Context) error
}

type dependencyValidateFunc func() error

func validateImageRegistryFunc(imageRegistry *v1.ImageRegistry) dependencyValidateFunc {
	return func() error {
		if imageRegistry.Spec.URL == "" {
			return errors.New("image registry url is empty")
		}

		if imageRegistry.Spec.Repository == "" {
			return errors.New("image registry repository is empty")
		}

		if imageRegistry.Status == nil || imageRegistry.Status.Phase != v1.ImageRegistryPhaseCONNECTED {
			return errors.New("image registry " + imageRegistry.Metadata.Name + " not connected")
		}

		return nil
	}
}

func validateClusterImageFunc(imageService registry.ImageService, registryAuth v1.ImageRegistryAuthConfig, image string) dependencyValidateFunc {
	return func() error {
		imageExisted, err := imageService.CheckImageExists(image, authn.FromConfig(authn.AuthConfig{
			Username:      registryAuth.Username,
			Password:      registryAuth.Password,
			Auth:          registryAuth.Auth,
			IdentityToken: registryAuth.IdentityToken,
			RegistryToken: registryAuth.IdentityToken,
		}))

		if err != nil {
			return err
		}

		if !imageExisted {
			return errors.Wrap(ErrImageNotFound, "image "+image+" not found")
		}

		return nil
	}
}

func getBaseImage(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry) (string, error) {
	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	return registryURL.Host + "/" + imageRegistry.Spec.Repository + "/neutree-serve:" + cluster.Spec.Version, nil
}

func generateRayClusterMetricsScrapeTargetsConfig(cluster *v1.Cluster, dashboardService dashboard.DashboardService) (*v1.MetricsScrapeTargetsConfig, error) {
	nodes, err := dashboardService.ListNodes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list ray nodes")
	}

	metricsScrapeTargetConfig := &v1.MetricsScrapeTargetsConfig{
		Labels: map[string]string{
			"ray_io_cluster": cluster.Metadata.Name,
			"job":            "ray",
		},
	}

	for _, node := range nodes {
		if node.Raylet.IsHeadNode {
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.DashboardMetricsPort))
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.AutoScaleMetricsPort))
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.RayletMetricsPort))

			continue
		}

		if node.Raylet.State == v1.AliveNodeState {
			metricsScrapeTargetConfig.Targets = append(metricsScrapeTargetConfig.Targets, fmt.Sprintf("%s:%d", node.IP, v1.RayletMetricsPort))
		}
	}

	return metricsScrapeTargetConfig, nil
}
