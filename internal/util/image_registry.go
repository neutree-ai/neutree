package util

import (
	"net/url"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func GetImageRegistryAuthInfo(r *v1.ImageRegistry) (string, string) {
	username := r.Spec.AuthConfig.Username

	password := ""

	switch {
	case r.Spec.AuthConfig.Password != "":
		password = r.Spec.AuthConfig.Password
	case r.Spec.AuthConfig.IdentityToken != "":
		password = r.Spec.AuthConfig.IdentityToken
	case r.Spec.AuthConfig.RegistryToken != "":
		password = r.Spec.AuthConfig.RegistryToken
	}

	return username, password
}

func GetImageRegistryHost(r *v1.ImageRegistry) (string, error) {
	parsedURL, err := url.Parse(r.Spec.URL)
	if err != nil {
		return "", err
	}

	return parsedURL.Host, nil
}

func GetImagePrefix(imageRegistry *v1.ImageRegistry) (string, error) {
	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	return registryURL.Host + "/" + imageRegistry.Spec.Repository, nil
}
