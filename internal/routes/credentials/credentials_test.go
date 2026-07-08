package credentials

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/internal/routes/proxies"
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
	assert.NotContains(t, paths, "/api/v1/credentials/external_endpoints",
		"external endpoint credentials must never be exported via the credentials API")
}
