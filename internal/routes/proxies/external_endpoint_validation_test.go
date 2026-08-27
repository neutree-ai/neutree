package proxies

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runExternalEndpointValidation sends one request through the validation
// middleware and reports whether the handler behind it ran, plus what the
// handler saw of the body -- the middleware reads the body, so restoring it is
// part of the contract.
func runExternalEndpointValidation(t *testing.T, method, body string) (*httptest.ResponseRecorder, bool, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	var (
		reached  bool
		seenBody string
	)

	engine := gin.New()
	engine.Handle(method, "/api/v1/external_endpoints", validateExternalEndpoint(), func(c *gin.Context) {
		reached = true

		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)

		seenBody = string(raw)

		c.Status(http.StatusCreated)
	})

	req := httptest.NewRequest(method, "/api/v1/external_endpoints", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	return recorder, reached, seenBody
}

func assertExternalEndpointNameRejected(t *testing.T, recorder *httptest.ResponseRecorder, reached bool) {
	t.Helper()

	assert.False(t, reached, "request must not reach the storage proxy")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)

	var payload validationError
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))

	assert.Equal(t, externalEndpointInvalidNameCode, payload.Code)
	assert.Equal(t, "invalid external endpoint name", payload.Message)
	assert.Contains(t, payload.Hint, "display_name")
}

func assertExternalEndpointPayloadRejected(t *testing.T, recorder *httptest.ResponseRecorder, reached bool) {
	t.Helper()

	assert.False(t, reached, "request must not reach the storage proxy")
	assert.Equal(t, http.StatusBadRequest, recorder.Code)

	var payload validationError
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))

	assert.Equal(t, externalEndpointInvalidPayloadCode, payload.Code)
	assert.Equal(t, "invalid external endpoint payload", payload.Message)
}

func TestValidateExternalEndpointCreateName(t *testing.T) {
	// The matrix NEU-714 asks for: a legal ASCII name, an ASCII space, Unicode
	// without a space, Unicode with a space, and the percent character that a
	// client would use to encode the others.
	cases := []struct {
		name     string
		resource string
		accepted bool
	}{
		{name: "ascii legal", resource: "external-openai-1", accepted: true},
		{name: "ascii legal with dots and underscores", resource: "ext.open_ai-1", accepted: true},
		{name: "ascii space", resource: "external endpoint", accepted: false},
		{name: "leading space", resource: " external", accepted: false},
		{name: "trailing space", resource: "external ", accepted: false},
		{name: "unicode without space", resource: "ëndpoint", accepted: false},
		{name: "unicode with space", resource: "ëndpoint spacer-2ea258e575", accepted: false},
		{name: "percent character", resource: "external%20endpoint", accepted: false},
		{name: "percent encoded unicode", resource: "%C3%ABndpoint", accepted: false},
		{name: "uppercase", resource: "External", accepted: false},
		{name: "slash", resource: "external/endpoint", accepted: false},
		{name: "question mark", resource: "external?x=1", accepted: false},
		{name: "leading hyphen", resource: "-external", accepted: false},
		{name: "empty", resource: "", accepted: false},
		{name: "too long", resource: strings.Repeat("a", 64), accepted: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"api_version": "v1",
				"kind":        "ExternalEndpoint",
				"metadata":    map[string]any{"workspace": "default", "name": tc.resource},
			})
			require.NoError(t, err)

			recorder, reached, seenBody := runExternalEndpointValidation(t, http.MethodPost, string(body))

			if tc.accepted {
				assert.True(t, reached)
				assert.JSONEq(t, string(body), seenBody, "the proxy behind the middleware must still see the body")

				return
			}

			assertExternalEndpointNameRejected(t, recorder, reached)
		})
	}
}

func TestValidateExternalEndpointCreateRequiresName(t *testing.T) {
	t.Run("metadata without a name", func(t *testing.T) {
		recorder, reached, _ := runExternalEndpointValidation(t, http.MethodPost,
			`{"api_version":"v1","metadata":{"workspace":"default"}}`)

		assertExternalEndpointNameRejected(t, recorder, reached)
	})

	t.Run("no metadata at all", func(t *testing.T) {
		recorder, reached, _ := runExternalEndpointValidation(t, http.MethodPost, `{"api_version":"v1"}`)

		assertExternalEndpointNameRejected(t, recorder, reached)
	})
}

func TestValidateExternalEndpointCreateBulk(t *testing.T) {
	t.Run("rejects an array where any name is illegal", func(t *testing.T) {
		recorder, reached, _ := runExternalEndpointValidation(t, http.MethodPost,
			`[{"metadata":{"name":"legal-one"}},{"metadata":{"name":"ëndpoint spacer"}}]`)

		assertExternalEndpointNameRejected(t, recorder, reached)
	})

	t.Run("accepts an array of legal names", func(t *testing.T) {
		_, reached, _ := runExternalEndpointValidation(t, http.MethodPost,
			`[{"metadata":{"name":"legal-one"}},{"metadata":{"name":"legal-two"}}]`)

		assert.True(t, reached)
	})
}

func TestValidateExternalEndpointPatch(t *testing.T) {
	t.Run("rejects a rename to an illegal name", func(t *testing.T) {
		recorder, reached, _ := runExternalEndpointValidation(t, http.MethodPatch,
			`{"metadata":{"name":"ëndpoint spacer-2ea258e575"}}`)

		assertExternalEndpointNameRejected(t, recorder, reached)
	})

	t.Run("allows a patch that does not touch the name", func(t *testing.T) {
		_, reached, _ := runExternalEndpointValidation(t, http.MethodPatch,
			`{"spec":{"timeout":30},"metadata":{"display_name":"Ëndpoint Rëname"}}`)

		assert.True(t, reached)
	})

	t.Run("allows a patch carrying no metadata", func(t *testing.T) {
		_, reached, _ := runExternalEndpointValidation(t, http.MethodPatch, `{"spec":{"timeout":30}}`)

		assert.True(t, reached)
	})

	// A resource named before this check existed still has to be deletable.
	t.Run("allows a soft delete regardless of the name it carries", func(t *testing.T) {
		_, reached, _ := runExternalEndpointValidation(t, http.MethodPatch,
			`{"metadata":{"name":"ëndpoint spacer-2ea258e575","deletion_timestamp":"2026-08-25T06:29:27Z"}}`)

		assert.True(t, reached)
	})
}

func TestValidateExternalEndpointPassesThroughOtherRequests(t *testing.T) {
	t.Run("ignores GET", func(t *testing.T) {
		_, reached, _ := runExternalEndpointValidation(t, http.MethodGet, "")

		assert.True(t, reached)
	})

	t.Run("leaves an empty body to postgrest", func(t *testing.T) {
		_, reached, _ := runExternalEndpointValidation(t, http.MethodPost, "")

		assert.True(t, reached)
	})

	t.Run("rejects metadata that is not an object", func(t *testing.T) {
		recorder, reached, _ := runExternalEndpointValidation(t, http.MethodPatch, `{"metadata": 5}`)

		assertExternalEndpointPayloadRejected(t, recorder, reached)
	})

	t.Run("rejects a name that is not a string", func(t *testing.T) {
		recorder, reached, _ := runExternalEndpointValidation(t, http.MethodPost, `{"metadata":{"name": 5}}`)

		assertExternalEndpointPayloadRejected(t, recorder, reached)
	})

	t.Run("rejects an array that is not an array of objects", func(t *testing.T) {
		recorder, reached, _ := runExternalEndpointValidation(t, http.MethodPost, `["legal-one"]`)

		assertExternalEndpointPayloadRejected(t, recorder, reached)
	})

	t.Run("rejects a body that is not an object", func(t *testing.T) {
		recorder, reached, _ := runExternalEndpointValidation(t, http.MethodPost, `"not-an-object"`)

		assertExternalEndpointPayloadRejected(t, recorder, reached)
	})
}
