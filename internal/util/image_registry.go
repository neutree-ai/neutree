package util

import (
	"net/url"

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
