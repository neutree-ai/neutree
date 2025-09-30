package cluster

import (
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/registry"
)

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
			return errors.New("image " + image + " not found")
		}

		return nil
	}
}
