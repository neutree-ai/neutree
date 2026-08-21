package registry

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/stretchr/testify/require"
)

const testImageManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","size":0,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"layers":[]}`

func TestImageServiceDoesNotExposeDeprecatedVersionDiscoveryMethods(t *testing.T) {
	serviceType := reflect.TypeOf((*ImageService)(nil)).Elem()
	for _, method := range []string{"CheckImageExists", "ListImageTags", "GetImageLabels"} {
		_, exists := serviceType.MethodByName(method)
		require.False(t, exists, "release-profile version discovery must not expose %s", method)
	}
}

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

func TestImageService_DefaultHTTPSDoesNotDowngradeToHTTP(t *testing.T) {
	server := httptest.NewServer(testRegistryHandler())
	t.Cleanup(server.Close)

	registryHost := strings.TrimPrefix(server.URL, "http://")
	service := NewImageService()

	allowed, err := service.CheckPullPermission(registryHost+"/neutree/router:v1.2.0", authn.Anonymous, false)
	require.Error(t, err)
	require.False(t, allowed)
}

func testRegistryHandler() http.Handler {
	manifestDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(testImageManifest)))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/neutree/router/manifests/v1.2.0":
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(testImageManifest)))
			w.Header().Set("Docker-Content-Digest", manifestDigest)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte(testImageManifest))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}
