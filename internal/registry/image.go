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

func CheckImageExists(image string, auth authn.Authenticator) (bool, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return false, errors.Wrap(err, "failed to parse image "+image)
	}

	_, err = remote.Head(ref, remote.WithAuth(auth), remote.WithTransport(&http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})) //nolint:gosec
	if err != nil {
		if transportErr, ok := err.(*transport.Error); ok {
			if transportErr.StatusCode == http.StatusNotFound {
				return false, nil
			}
		}

		return false, errors.Wrap(err, "failed to request image "+image)
	}

	return true, nil
}

func ListImageTags(imageRepo string, auth authn.Authenticator) ([]string, error) {
	repo, err := name.NewRepository(imageRepo)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse image repo "+imageRepo)
	}

	tags, err := remote.List(repo, remote.WithAuth(auth), remote.WithTransport(&http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}})) //nolint:gosec
	if err != nil {
		if transportErr, ok := err.(*transport.Error); ok {
			if transportErr.StatusCode == http.StatusNotFound {
				return nil, nil
			}
		}

		return nil, errors.Wrap(err, "failed to list the image tags of image repo "+imageRepo)
	}

	return tags, nil
}
