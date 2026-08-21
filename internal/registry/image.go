package registry

import (
	"crypto/tls"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/pkg/errors"
)

type ImageService interface {
	// CheckPullPermission checks if the provided auth has pull permission for the image
	CheckPullPermission(image string, auth authn.Authenticator, useHTTP bool) (bool, error)
}

type imageService struct {
	transport http.RoundTripper
}

func NewImageService() ImageService {
	return &imageService{
		transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
}

func (svc *imageService) CheckPullPermission(image string, auth authn.Authenticator, useHTTP bool) (bool, error) {
	ref, err := name.ParseReference(image, registryNameOptions(useHTTP)...)
	if err != nil {
		return false, errors.Wrap(err, "failed to parse image "+image)
	}

	_, err = remote.Head(ref, remote.WithAuth(auth), remote.WithTransport(svc.registryTransport(ref.Context().Registry.RegistryStr(), useHTTP)))
	if err != nil {
		if transportErr, ok := err.(*transport.Error); ok {
			if transportErr.StatusCode == http.StatusUnauthorized || transportErr.StatusCode == http.StatusForbidden {
				return false, nil
			}

			// If the image does not exist, we consider that pull permission is granted
			if transportErr.StatusCode == http.StatusNotFound {
				return true, nil
			}
		}

		return false, errors.Wrap(err, "failed to request image "+image)
	}

	return true, nil
}

func registryNameOptions(useHTTP bool) []name.Option {
	if useHTTP {
		return []name.Option{name.Insecure}
	}

	return nil
}

func (svc *imageService) registryTransport(registry string, useHTTP bool) http.RoundTripper {
	if useHTTP {
		return svc.transport
	}

	return &forceHTTPSRegistryTransport{inner: svc.transport, registry: registry}
}

type forceHTTPSRegistryTransport struct {
	inner    http.RoundTripper
	registry string
}

func (t *forceHTTPSRegistryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "http" || request.URL.Host != t.registry {
		return t.inner.RoundTrip(request)
	}

	request = request.Clone(request.Context())
	request.URL.Scheme = "https"

	return t.inner.RoundTrip(request)
}
