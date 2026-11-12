package proxies

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestValidateClusterRouterProxyInput(t *testing.T) {
	cases := []struct {
		name    string
		version string
		router  string
		wantErr string
	}{
		{name: "old router without options", version: "v1.2.0", router: `{}`},
		{name: "old router with null options", version: "v1.2.1", router: `{"connect_timeout_seconds":null}`},
		{
			name: "release supports explicit zero and fractional values", version: "v1.2.2",
			router: `{"max_connections_per_backend":0,"connect_timeout_seconds":0,"sock_connect_timeout_seconds":0.25,"sock_read_timeout_seconds":1.5}`,
		},
		{
			name: "later release supports upper limits", version: "v1.3.1",
			router: `{"max_connections_per_backend":65535,"connect_timeout_seconds":604800,"sock_connect_timeout_seconds":604800,"sock_read_timeout_seconds":604800}`,
		},
		{name: "old router rejects explicit zero", version: "v1.2.0", router: `{"connect_timeout_seconds":0}`, wantErr: "greater than v1.2.1"},
		{name: "boundary release is excluded", version: "v1.2.1", router: `{"connect_timeout_seconds":1}`, wantErr: "greater than v1.2.1"},
		{name: "build metadata does not exceed boundary", version: "v1.2.1+build.1", router: `{"connect_timeout_seconds":1}`, wantErr: "greater than v1.2.1"},
		{name: "boundary prerelease is excluded", version: "v1.2.1-rc.1", router: `{"connect_timeout_seconds":1}`, wantErr: "greater than v1.2.1"},
		{name: "next patch prerelease exceeds boundary", version: "v1.2.2-rc.1", router: `{"connect_timeout_seconds":1}`},
		{name: "current test image exceeds boundary", version: "v1.3.0-neu869-0", router: `{"connect_timeout_seconds":1}`},
		{name: "new router override on old cluster", version: "v1.2.1", router: `{"version":"v1.3.0-neu869-0","connect_timeout_seconds":1}`},
		{
			name: "old router override on new cluster", version: "v1.3.0",
			router: `{"version":"v1.2.1","connect_timeout_seconds":1}`, wantErr: "greater than v1.2.1",
		},
		{name: "custom router tag", version: "v1.3.0", router: `{"version":"custom","connect_timeout_seconds":1}`, wantErr: "router.version"},
		{name: "router tag with whitespace", version: "v1.3.0", router: `{"version":" v1.3.0 ","connect_timeout_seconds":1}`, wantErr: "router.version"},
		{name: "negative pool size", version: "v1.3.0", router: `{"max_connections_per_backend":-1}`, wantErr: "max_connections_per_backend"},
		{name: "pool size over limit", version: "v1.3.0", router: `{"max_connections_per_backend":65536}`, wantErr: "max_connections_per_backend"},
		{name: "negative connection timeout", version: "v1.3.0", router: `{"connect_timeout_seconds":-0.1}`, wantErr: "connect_timeout_seconds"},
		{name: "connection timeout over limit", version: "v1.3.0", router: `{"connect_timeout_seconds":604800.1}`, wantErr: "connect_timeout_seconds"},
		{name: "negative socket connect timeout", version: "v1.3.0", router: `{"sock_connect_timeout_seconds":-1}`, wantErr: "sock_connect_timeout_seconds"},
		{name: "socket connect timeout over limit", version: "v1.3.0", router: `{"sock_connect_timeout_seconds":604801}`, wantErr: "sock_connect_timeout_seconds"},
		{name: "negative socket read timeout", version: "v1.3.0", router: `{"sock_read_timeout_seconds":-1}`, wantErr: "sock_read_timeout_seconds"},
		{name: "socket read timeout over limit", version: "v1.3.0", router: `{"sock_read_timeout_seconds":604801}`, wantErr: "sock_read_timeout_seconds"},
		{name: "cleared fields allow old override", version: "v1.3.0", router: `{"version":"v1.2.1","connect_timeout_seconds":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"spec":{"type":"kubernetes","version":%q,"config":{"kubernetes_config":{"router":%s}}}}`, tc.version, tc.router)
			var cluster v1.Cluster
			require.NoError(t, json.Unmarshal([]byte(body), &cluster))
			validationErr := validateClusterRouterProxyInput(&ValidationInput[v1.Cluster]{
				New: &cluster, Operation: clusterValidationCreate,
			})
			if tc.wantErr != "" {
				require.NotNil(t, validationErr)
				assert.Contains(t, validationErr.Hint, tc.wantErr)
				return
			}
			assert.Nil(t, validationErr)
		})
	}
}

func TestReadClusterRouterProxyInputRejectsInvalidTypes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name   string
		router string
	}{
		{name: "fractional pool size", router: `{"max_connections_per_backend":1.5}`},
		{name: "timeout supplied as string", router: `{"sock_read_timeout_seconds":"60"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"spec":{"config":{"kubernetes_config":{"router":%s}}}}`, tc.router)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/clusters", strings.NewReader(body))
			_, validationErr := readClusterValidationInput(ctx)
			require.NotNil(t, validationErr)
			assert.Equal(t, "invalid cluster payload", validationErr.Message)
		})
	}
}
