package util

import (
	"net/url"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func GetImagePrefix(imageRegistry *v1.ImageRegistry) (string, error) {
	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	return registryURL.Host + "/" + imageRegistry.Spec.Repository, nil
}
