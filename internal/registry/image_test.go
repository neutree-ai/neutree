package registry

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/stretchr/testify/require"
)

const testImageConfig = `{"architecture":"amd64","os":"linux","config":{"Labels":{"neutree.ai/cluster-version":"v1.2.0"}},"rootfs":{"type":"layers","diff_ids":[]}}`

func TestImageService_CheckPullPermissionUsesConfiguredScheme(t *testing.T) {
	tests := []struct {
		name      string
		newServer func(http.Handler) *httptest.Server
		useHTTP   bool
		scheme    string
	}{
		{name: "explicit HTTP", newServer: httptest.NewServer, useHTTP: true, scheme: "http"},
		{name: "HTTPS by default", newServer: httptest.NewTLSServer, scheme: "https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := tt.newServer(testRegistryHandler())
			t.Cleanup(server.Close)

			image := strings.TrimPrefix(server.URL, tt.scheme+"://") + "/neutree/router:v1.2.0"
			allowed, err := NewImageService().CheckPullPermission(image, authn.Anonymous, tt.useHTTP)

			require.NoError(t, err)
			require.True(t, allowed)
		})
	}
}

func TestImageService_ListImageTagsUsesConfiguredScheme(t *testing.T) {
	tests := []struct {
		name      string
		newServer func(http.Handler) *httptest.Server
		useHTTP   bool
		scheme    string
	}{
		{name: "explicit HTTP", newServer: httptest.NewServer, useHTTP: true, scheme: "http"},
		{name: "HTTPS by default", newServer: httptest.NewTLSServer, scheme: "https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := tt.newServer(testRegistryHandler())
			t.Cleanup(server.Close)

			repository := strings.TrimPrefix(server.URL, tt.scheme+"://") + "/neutree/router"
			tags, err := NewImageService().ListImageTags(repository, authn.Anonymous, tt.useHTTP)

			require.NoError(t, err)
			require.Equal(t, []string{"v1.2.0"}, tags)
		})
	}
}

func TestImageService_GetImageLabelsUsesConfiguredScheme(t *testing.T) {
	tests := []struct {
		name      string
		newServer func(http.Handler) *httptest.Server
		useHTTP   bool
		scheme    string
	}{
		{name: "explicit HTTP", newServer: httptest.NewServer, useHTTP: true, scheme: "http"},
		{name: "HTTPS by default", newServer: httptest.NewTLSServer, scheme: "https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := tt.newServer(testRegistryHandler())
			t.Cleanup(server.Close)

			image := strings.TrimPrefix(server.URL, tt.scheme+"://") + "/neutree/router:v1.2.0"
			labels, err := NewImageService().GetImageLabels(image, authn.Anonymous, tt.useHTTP)

			require.NoError(t, err)
			require.Equal(t, "v1.2.0", labels["neutree.ai/cluster-version"])
		})
	}
}

func TestImageService_DefaultHTTPSDoesNotDowngradeToHTTP(t *testing.T) {
	server := httptest.NewServer(testRegistryHandler())
	t.Cleanup(server.Close)

	registryHost := strings.TrimPrefix(server.URL, "http://")
	service := NewImageService()

	allowed, err := service.CheckPullPermission(registryHost+"/neutree/router:v1.2.0", authn.Anonymous, false)
	require.Error(t, err)
	require.False(t, allowed)

	_, err = service.ListImageTags(registryHost+"/neutree/router", authn.Anonymous, false)
	require.Error(t, err)

	_, err = service.GetImageLabels(registryHost+"/neutree/router:v1.2.0", authn.Anonymous, false)
	require.Error(t, err)
}

func testRegistryHandler() http.Handler {
	configDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(testImageConfig)))
	manifest := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","size":%d,"digest":"%s"},"layers":[]}`,
		len(testImageConfig), configDigest)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/neutree/router/tags/list":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"neutree/router","tags":["v1.2.0"]}`))
		case "/v2/neutree/router/manifests/v1.2.0":
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(manifest)))
			w.Header().Set("Docker-Content-Digest", fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(manifest))))
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte(manifest))
			}
		case "/v2/neutree/router/blobs/" + configDigest:
			w.Header().Set("Content-Type", "application/vnd.oci.image.config.v1+json")
			_, _ = w.Write([]byte(testImageConfig))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}
