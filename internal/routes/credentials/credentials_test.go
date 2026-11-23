package credentials

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/neutree-ai/neutree/internal/routes/proxies"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

type closeNotifyRecorder struct {
	*httptest.ResponseRecorder
}

func (r *closeNotifyRecorder) CloseNotify() <-chan bool {
	return make(chan bool)
}

func TestHandleResourceCredentialsForwardsRequestUnmodified(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var capturedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	w := &closeNotifyRecorder{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(w)
	// No user_id set: shared infrastructure credentials are governed by
	// RBAC (RequirePermission on the route), not by an owner filter.
	c.Request = httptest.NewRequest(
		http.MethodGet,
		"/credentials/model_registries?metadata-%3E%3Eworkspace=eq.default",
		nil,
	)

	handler := handleResourceCredentials(&proxies.Dependencies{
		StorageAccessURL: server.URL,
	}, "model_registries")
	handler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, `metadata-%3E%3Eworkspace=eq.default`, capturedQuery)
}

func TestRegisterCredentialsRoutesOmitsExternalEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	RegisterCredentialsRoutes(router.Group("/api/v1"), nil, &Dependencies{})

	var paths []string
	for _, r := range router.Routes() {
		paths = append(paths, r.Path)
	}

	assert.Contains(t, paths, "/api/v1/credentials/clusters")
	assert.Contains(t, paths, "/api/v1/credentials/image_registries")
	assert.Contains(t, paths, "/api/v1/credentials/model_registries")
	assert.Contains(t, paths, "/api/v1/credentials/log_sinks")
	assert.NotContains(t, paths, "/api/v1/credentials/external_endpoints",
		"external endpoint credentials must never be exported via the credentials API")
}

func TestLogSinkCredentialsRequirePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		hasPermission  bool
		wantStatus     int
		wantForwarding bool
	}{
		{name: "with read-credentials", hasPermission: true, wantStatus: http.StatusOK, wantForwarding: true},
		{name: "without read-credentials", hasPermission: false, wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var forwarded bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded = true
				assert.Equal(t, "/log_sinks", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"spec":{"victorialogs":{"auth":{"type":"bearer","token":"secret"}}}},` +
					`{"spec":{"s3":{"auth":{"access_key_id":"ak","secret_access_key":"sk"}}}}]`))
			}))
			defer upstream.Close()

			s := storageMocks.NewMockStorage(t)
			s.EXPECT().
				CallDatabaseFunction("has_permission", mock.MatchedBy(func(params map[string]interface{}) bool {
					return params["required_permission"] == "log_sink:read-credentials" && params["workspace"] == nil
				}), mock.Anything).
				Run(func(_ string, _ map[string]interface{}, result interface{}) {
					*result.(*bool) = tt.hasPermission
				}).
				Return(nil)

			router := gin.New()
			setUser := func(c *gin.Context) { c.Set("user_id", "u1") }
			RegisterCredentialsRoutes(router.Group("/api/v1"), []gin.HandlerFunc{setUser}, &Dependencies{
				Storage:          s,
				StorageAccessURL: upstream.URL,
			})

			w := &closeNotifyRecorder{ResponseRecorder: httptest.NewRecorder()}
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/credentials/log_sinks?metadata-%3E%3Ename=eq.vl", nil))

			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Equal(t, tt.wantForwarding, forwarded)
			if tt.wantForwarding {
				assert.Contains(t, w.Body.String(), `"token":"secret"`)
				assert.Contains(t, w.Body.String(), `"access_key_id":"ak","secret_access_key":"sk"`)
			}
		})
	}
}
