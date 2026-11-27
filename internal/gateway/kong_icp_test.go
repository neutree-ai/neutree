package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kong/go-kong/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.openly.dev/pointy"
)

func TestInferenceRouteReconcilesBuffering(t *testing.T) {
	for _, tc := range []struct {
		name              string
		exists            bool
		request, response *bool
	}{
		{"create", false, nil, nil}, {"missing fields", true, nil, nil},
		{"request buffered", true, pointy.Bool(true), pointy.Bool(false)},
		{"response buffered", true, pointy.Bool(false), pointy.Bool(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const path = "/workspace/ws/endpoint/ep"
			svc := &kong.Service{ID: pointy.String("svc")}
			route := kong.Route{ID: pointy.String("route"), Paths: []*string{pointy.String(path)}, Service: svc,
				RequestBuffering: tc.request, ResponseBuffering: tc.response}
			exists, writes := tc.exists, 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet && !exists {
					http.Error(w, `{"message":"Not found"}`, 404)
					return
				}
				if r.Method != http.MethodGet {
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&route))
					exists = true
					writes++
				}
				assert.NoError(t, json.NewEncoder(w).Encode(route))
			}))
			defer api.Close()
			client, err := kong.NewClient(pointy.String(api.URL), api.Client())
			require.NoError(t, err)
			gw := &Kong{kongClient: client}
			for i := 0; i < 2; i++ {
				_, err := gw.syncInferenceRoute("route", path, svc)
				require.NoError(t, err)
			}
			got, err := client.Routes.Get(context.Background(), pointy.String("route"))
			require.NoError(t, err)
			require.Equal(t, pointy.Bool(false), got.RequestBuffering)
			require.Equal(t, pointy.Bool(false), got.ResponseBuffering)
			require.Equal(t, 1, writes)
		})
	}
}

func TestInternalPreFunctionConfiguration(t *testing.T) {
	for _, token := range []string{"", "key\"\\secret"} {
		plugin := (&Kong{internalToken: token}).generateRewriteApiKeyHeaderPlugin()
		access := plugin.Config["access"].([]string)
		require.Len(t, access, 2)
		require.True(t, strings.HasPrefix(access[0], "local expected = "+strconv.Quote(token)+"\n"))
		require.Contains(t, access[1], "kong_apikey")
		require.NotEmpty(t, plugin.Config["header_filter"])
	}
}
