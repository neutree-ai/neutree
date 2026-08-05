package proxies

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// newAPIKeyForceDeleteRouter mounts the middleware in front of a stub handler so
// tests can observe both the rejection and the pass-through path.
func newAPIKeyForceDeleteRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	stub := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"proxied": true})
	}

	r.GET("/api_keys", rejectAPIKeyForceDelete(), stub)
	r.PATCH("/api_keys", rejectAPIKeyForceDelete(), stub)

	return r
}

func TestRejectAPIKeyForceDelete(t *testing.T) {
	softDelete := `{"metadata":{"name":"k","workspace":"default","deletion_timestamp":"2026-08-05T07:20:48Z"}}`
	forceSoftDelete := `{"metadata":{"name":"k","workspace":"default","deletion_timestamp":"2026-08-05T07:20:48Z",` +
		`"annotations":{"neutree.ai/force-delete":"true"}}}`

	tests := []struct {
		name         string
		method       string
		body         string
		expectStatus int
		expectCode   string
	}{
		{
			name:         "plain soft delete passes through",
			method:       http.MethodPatch,
			body:         softDelete,
			expectStatus: http.StatusOK,
		},
		{
			name:         "ordinary update passes through",
			method:       http.MethodPatch,
			body:         `{"spec":{"limits":{"rps":10}}}`,
			expectStatus: http.StatusOK,
		},
		{
			name:         "force delete is rejected",
			method:       http.MethodPatch,
			body:         forceSoftDelete,
			expectStatus: http.StatusBadRequest,
			expectCode:   apiKeyForceDeleteRejectedCode,
		},
		{
			name:   "force-delete annotation is rejected even without a deletion timestamp",
			method: http.MethodPatch,
			body: `{"metadata":{"name":"k","workspace":"default",` +
				`"annotations":{"neutree.ai/force-delete":"true"}}}`,
			expectStatus: http.StatusBadRequest,
			expectCode:   apiKeyForceDeleteRejectedCode,
		},
		{
			name:         "force delete inside a bulk array is rejected",
			method:       http.MethodPatch,
			body:         "[" + softDelete + "," + forceSoftDelete + "]",
			expectStatus: http.StatusBadRequest,
			expectCode:   apiKeyForceDeleteRejectedCode,
		},
		{
			name:         "annotation set to a value other than true passes through",
			method:       http.MethodPatch,
			body:         `{"metadata":{"name":"k","annotations":{"neutree.ai/force-delete":"false"}}}`,
			expectStatus: http.StatusOK,
		},
		{
			name:         "other annotations pass through",
			method:       http.MethodPatch,
			body:         `{"metadata":{"name":"k","annotations":{"neutree.ai/other":"true"}}}`,
			expectStatus: http.StatusOK,
		},
		{
			name:         "empty body passes through",
			method:       http.MethodPatch,
			body:         "",
			expectStatus: http.StatusOK,
		},
		{
			name:         "unparsable body is left to postgrest",
			method:       http.MethodPatch,
			body:         `{"metadata":`,
			expectStatus: http.StatusOK,
		},
		{
			name:         "reads are not inspected",
			method:       http.MethodGet,
			body:         "",
			expectStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newAPIKeyForceDeleteRouter()

			req := httptest.NewRequest(tt.method, "/api_keys?id=eq.1", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.expectStatus, w.Code)

			if tt.expectCode == "" {
				return
			}

			var got validationError
			assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, tt.expectCode, got.Code)
			assert.Contains(t, got.Message, "force delete")
		})
	}
}

// TestRejectAPIKeyForceDeletePreservesBody guards the read-and-restore: the
// downstream proxy must still see the original payload.
func TestRejectAPIKeyForceDeletePreservesBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := `{"metadata":{"name":"k","deletion_timestamp":"2026-08-05T07:20:48Z"}}`

	var seen string

	r := gin.New()
	r.PATCH("/api_keys", rejectAPIKeyForceDelete(), func(c *gin.Context) {
		raw, err := c.GetRawData()
		assert.NoError(t, err)

		seen = string(raw)

		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPatch, "/api_keys?id=eq.1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, body, seen)
}
