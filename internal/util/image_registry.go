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

// parseRegistryHost normalizes a registry URL by stripping any scheme and
// returning only the host (with optional port). It handles both formats:
// "registry.example.com:5000" and "https://registry.example.com:5000".
func parseRegistryHost(rawURL string) (string, error) {
	// Strip scheme if present
	stripped := rawURL
	if strings.HasPrefix(rawURL, "https://") || strings.HasPrefix(rawURL, "http://") {
		stripped = strings.TrimPrefix(rawURL, "https://")
		stripped = strings.TrimPrefix(stripped, "http://")
	}

	// Remove any trailing path/slash
	stripped = strings.TrimRight(stripped, "/")

	if stripped == "" {
		return "", errors.New("empty registry host")
	}

	// Validate by prepending a dummy scheme and parsing
	parsed, err := url.Parse("dummy://" + stripped)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse registry url")
	}

	if parsed.Host == "" {
		return "", errors.New("invalid registry url: " + rawURL)
	}

	return parsed.Host, nil
}

// NormalizeRegistryHost strips any scheme from a registry address and returns
// the bare host:port. Use this to sanitize user-provided registry addresses
// before passing them to Docker commands or image name construction.
func NormalizeRegistryHost(rawURL string) (string, error) {
	return parseRegistryHost(rawURL)
}

func GetImageRegistryHost(r *v1.ImageRegistry) (string, error) {
	return parseRegistryHost(r.Spec.URL)
}

func GetImagePrefix(imageRegistry *v1.ImageRegistry) (string, error) {
	host, err := parseRegistryHost(imageRegistry.Spec.URL)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	if imageRegistry.Spec.Repository == "" {
		return host, nil
	}

	return host + "/" + imageRegistry.Spec.Repository, nil
}
