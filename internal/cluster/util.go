package cluster

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

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

func generateInstallNs(cluster *v1.Cluster) *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: util.ClusterNamespace(cluster),
			Labels: map[string]string{
				v1.NeutreeClusterLabelKey:          cluster.Metadata.Name,
				v1.NeutreeClusterWorkspaceLabelKey: cluster.Metadata.Workspace,
			},
		},
	}
}

func generateImagePullSecret(ns string, imageRegistry *v1.ImageRegistry) (*corev1.Secret, error) {
	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse image registry url: %s", imageRegistry.Spec.URL)
	}

	var password string

	switch {
	case imageRegistry.Spec.AuthConfig.Password != "":
		password = imageRegistry.Spec.AuthConfig.Password
	case imageRegistry.Spec.AuthConfig.IdentityToken != "":
		password = imageRegistry.Spec.AuthConfig.IdentityToken
	case imageRegistry.Spec.AuthConfig.RegistryToken != "":
		password = imageRegistry.Spec.AuthConfig.RegistryToken
	}

	userName := removeEscapes(imageRegistry.Spec.AuthConfig.Username)
	password = removeEscapes(password)
	auth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s",
		userName,
		password)))

	dockerAuthData := fmt.Sprintf(`{
			"auths": {
				"%s": {
					"username": "%s",
					"password": "%s",
					"auth": "%s"
				}
			}
		}`, registryURL.Host,
		userName,
		password,
		auth)

	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ImagePullSecretName,
			Namespace: ns,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(dockerAuthData),
		},
	}, nil
}

func getUsedImageRegistries(cluster *v1.Cluster, s storage.Storage) (*v1.ImageRegistry, error) {
	imageRegistryFilter := []storage.Filter{
		{
			Column:   "metadata->name",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Spec.ImageRegistry),
		},
	}

	if cluster.Metadata.Workspace != "" {
		imageRegistryFilter = append(imageRegistryFilter, storage.Filter{
			Column:   "metadata->workspace",
			Operator: "eq",
			Value:    fmt.Sprintf(`"%s"`, cluster.Metadata.Workspace),
		})
	}

	imageRegistryList, err := s.ListImageRegistry(storage.ListOption{Filters: imageRegistryFilter})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list image registry")
	}

	if len(imageRegistryList) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	return &imageRegistryList[0], nil
}

func removeEscapes(s string) string {
	re := regexp.MustCompile(`\\`)
	return re.ReplaceAllString(s, "")
}
