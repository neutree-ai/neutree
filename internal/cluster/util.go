package cluster

import (
	"encoding/base64"
	"fmt"
	"math"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

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
	host, err := util.GetImageRegistryHost(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image registry host")
	}

	user, token, err := util.GetImageRegistryAuthInfo(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image registry auth info")
	}

	auth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s",
		user,
		token)))

	dockerAuthData := fmt.Sprintf(`{
			"auths": {
				"%s": {
					"username": "%s",
					"password": "%s",
					"auth": "%s"
				}
			}
		}`, host,
		user,
		token,
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

	targetImageRegistry := &imageRegistryList[0]
	if targetImageRegistry.Status == nil || targetImageRegistry.Status.Phase != v1.ImageRegistryPhaseCONNECTED {
		return nil, errors.New("image registry " + cluster.Spec.ImageRegistry + " not ready")
	}

	return targetImageRegistry, nil
}

func roundFloat64ToTwoDecimals(input float64) float64 {
	return math.Round(input*100) / 100
}
