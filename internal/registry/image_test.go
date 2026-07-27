package registry

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/stretchr/testify/require"
)

func TestImageService_CheckPullPermission_UsesHTTPForInsecureRegistry(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method == http.MethodHead && r.URL.Path == "/v2/neutree/neutree-serve/manifests/latest" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(registry.Close)

	image := strings.TrimPrefix(registry.URL, "http://") + "/neutree/neutree-serve:latest"
	hasPermission, err := NewImageService().CheckPullPermission(image, authn.Anonymous, true)

	require.NoError(t, err)
	require.True(t, hasPermission)
}

func TestImageService_CheckPullPermission_UsesHTTPSWhenHTTPIsNotExplicit(t *testing.T) {
	registry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method == http.MethodHead && r.URL.Path == "/v2/neutree/neutree-serve/manifests/latest" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(registry.Close)

	image := strings.TrimPrefix(registry.URL, "https://") + "/neutree/neutree-serve:latest"
	hasPermission, err := NewImageService().CheckPullPermission(image, authn.Anonymous, false)

	require.NoError(t, err)
	require.True(t, hasPermission)
}

func TestImageService_CheckPullPermission_DoesNotFallbackToHTTP(t *testing.T) {
	transport := &recordingRoundTripper{}
	service := &imageService{transport: transport}

	hasPermission, err := service.CheckPullPermission("127.0.0.1:5000/neutree/neutree-serve:latest", authn.Anonymous, false)

	require.NoError(t, err)
	require.True(t, hasPermission)
	require.NotContains(t, transport.schemes, "http")
}

type recordingRoundTripper struct {
	schemes []string
}

func (t *recordingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	t.schemes = append(t.schemes, request.URL.Scheme)
	if len(t.schemes) == 1 && request.URL.Path == "/v2/" {
		return nil, errors.New("HTTPS unavailable")
	}

	statusCode := http.StatusOK
	if request.Method == http.MethodHead {
		statusCode = http.StatusNotFound
	}

	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}, nil
}
