package util

import (
	"encoding/base64"
	"net/url"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// GetImageRegistryAuthInfo extracts the username and password from the given image registry auth config.
// If both username and password are provided, they are returned directly.
// If auth is provided, it is base64 decoded to extract the username and password.
// If neither is provided or they do not meet the specifications, an empty string is returned.
func GetImageRegistryAuthInfo(r *v1.ImageRegistry) (string, string, error) {
	if r == nil || r.Spec == nil {
		return "", "", errors.New("image registry spec is nil")
	}

	if r.Spec.AuthConfig.Username != "" && r.Spec.AuthConfig.Password != "" {
		return r.Spec.AuthConfig.Username, r.Spec.AuthConfig.Password, nil
	}

	if r.Spec.AuthConfig.Auth != "" {
		base64Decoded, err := base64.StdEncoding.DecodeString(r.Spec.AuthConfig.Auth)
		if err != nil {
			return "", "", errors.Wrap(err, "failed to decode image registry auth")
		}

		decodeString := string(base64Decoded)

		splits := strings.SplitN(decodeString, ":", 2)
		if len(splits) != 2 {
			return "", "", errors.New("invalid image registry auth format")
		}

		if splits[0] == "" || splits[1] == "" {
			return "", "", nil
		}

		return splits[0], splits[1], nil
	}

	return "", "", nil
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

	if registryURL.Host == "" {
		return "", errors.New("invalid image registry url: " + imageRegistry.Spec.URL)
	}

	if imageRegistry.Spec.Repository == "" {
		return registryURL.Host, nil
	}

	return registryURL.Host + "/" + imageRegistry.Spec.Repository, nil
}
