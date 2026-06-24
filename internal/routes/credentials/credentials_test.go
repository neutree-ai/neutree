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

func TestHandleResourceCredentialsFiltersByOwner(t *testing.T) {
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
	c.Set("user_id", "owner-user")
	c.Request = httptest.NewRequest(
		http.MethodGet,
		"/credentials/model_registries?metadata-%3Eworkspace=eq.%22default%22&metadata-%3Elabels-%3E%3Eneutree.ai%2Fcredential-owner=eq.spoofed-user",
		nil,
	)

	handler := handleResourceCredentials(&proxies.Dependencies{
		StorageAccessURL: server.URL,
	}, "model_registries")
	handler(c)

	assert.Equal(t, http.StatusOK, w.Code)

	query := c.Request.URL.Query()
	assert.Equal(t, `eq."default"`, query.Get("metadata->workspace"))
	assert.Equal(t, "eq.owner-user", query.Get("metadata->labels->>neutree.ai/credential-owner"))
	assert.Contains(t, capturedQuery, "metadata-%3Elabels-%3E%3Eneutree.ai%2Fcredential-owner=eq.owner-user")
}

func TestHandleResourceCredentialsRequiresUser(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := &closeNotifyRecorder{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/credentials/model_registries", nil)

	handler := handleResourceCredentials(&proxies.Dependencies{
		StorageAccessURL: "http://example.com",
	}, "model_registries")
	handler(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
