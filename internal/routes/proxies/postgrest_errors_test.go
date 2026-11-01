package proxies

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgrestProxyReportsBrokenConnectionAs503(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
	}{
		{"connection lost mid-request", http.StatusBadRequest, `{"code":"","details":null,"hint":null,"message":""}`, http.StatusServiceUnavailable},
		{"real client error", http.StatusBadRequest, `{"code":"PGRST100","message":"bad filter"}`, http.StatusBadRequest},
		{"non-JSON 400", http.StatusBadRequest, `bad request`, http.StatusBadRequest},
		{"success", http.StatusOK, `{"code":""}`, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()

			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.POST("/rpc/record_api_usage", CreatePostgrestProxyHandler(upstream.URL, "rpc/record_api_usage", nil))

			api := httptest.NewServer(router)
			defer api.Close()

			resp, err := http.Post(api.URL+"/rpc/record_api_usage", "application/json", nil)
			require.NoError(t, err)
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, resp.StatusCode)
			assert.Equal(t, tc.body, string(body), "body is passed through")
		})
	}
}
