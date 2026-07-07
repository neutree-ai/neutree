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
		"/credentials/model_registries?metadata-%3Eworkspace=eq.%22default%22",
		nil,
	)

	handler := handleResourceCredentials(&proxies.Dependencies{
		StorageAccessURL: server.URL,
	}, "model_registries")
	handler(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, `metadata-%3Eworkspace=eq.%22default%22`, capturedQuery)
}
