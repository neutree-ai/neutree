package proxies

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

const validLDAPIdentitySource = `{
	"api_version":"v1",
	"kind":"IdentitySource",
	"metadata":{"name":"corp","display_name":"Corp LDAP"},
	"spec":{"type":"ldap","enabled":true,"ldap":{
		"url":"ldaps://ldap.example.org","bind_dn":"cn=svc","bind_password":"pw",
		"user_base_dn":"ou=people","user_filter":"(uid={username})"}}
}`

type identitySourceUpstream struct {
	server *httptest.Server
	called atomic.Bool
	body   atomic.Value
}

func newIdentitySourceUpstream(t *testing.T, status int, response string) *identitySourceUpstream {
	t.Helper()

	u := &identitySourceUpstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.called.Store(true)
		assert.Equal(t, "/identity_sources", r.URL.Path)

		body, _ := io.ReadAll(r.Body)
		u.body.Store(string(body))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(u.server.Close)

	return u
}

func serveIdentitySource(t *testing.T, deps *Dependencies, method, target, body string) *closeNotifyRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)

	router := gin.New()
	RegisterIdentitySourceRoutes(router.Group("/api/v1"), nil, deps)

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")

	rec := newCloseNotifyRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func TestIdentitySourceCreateValidation(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantHint string
	}{
		{"name with a dot", strings.Replace(validLDAPIdentitySource, `"name":"corp"`, `"name":"corp.ldap"`, 1), "lowercase letters"},
		{"no metadata", `{"spec":{"type":"ldap"}}`, "metadata is required"},
		{"no spec", `{"metadata":{"name":"corp"}}`, "spec is required"},
		{"unknown type", `{"metadata":{"name":"corp"},"spec":{"type":"saml"}}`, "spec.type"},
		{"filter without placeholder", strings.Replace(validLDAPIdentitySource, "(uid={username})", "(uid=*)", 1), "{username}"},
		{"array body", `[` + validLDAPIdentitySource + `]`, "single identity source"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := newIdentitySourceUpstream(t, http.StatusCreated, "")

			rec := serveIdentitySource(t, &Dependencies{StorageAccessURL: upstream.server.URL},
				http.MethodPost, "/api/v1/identity_sources", tt.body)

			assert.Equal(t, http.StatusBadRequest, rec.ResponseRecorder.Code)
			assert.Contains(t, rec.ResponseRecorder.Body.String(), `"code":"10261"`)
			assert.Contains(t, rec.ResponseRecorder.Body.String(), tt.wantHint)
			assert.False(t, upstream.called.Load(), "an invalid identity source must not reach PostgREST")
		})
	}
}

func TestIdentitySourceCreateForwardsValid(t *testing.T) {
	upstream := newIdentitySourceUpstream(t, http.StatusCreated, "")

	rec := serveIdentitySource(t, &Dependencies{StorageAccessURL: upstream.server.URL},
		http.MethodPost, "/api/v1/identity_sources", validLDAPIdentitySource)

	assert.Equal(t, http.StatusCreated, rec.ResponseRecorder.Code)
	require.True(t, upstream.called.Load())
	// The secret goes to the database, which encrypts it.
	assert.Contains(t, upstream.body.Load(), `"bind_password":"pw"`)
}

func TestIdentitySourceReadMasksSecrets(t *testing.T) {
	upstream := newIdentitySourceUpstream(t, http.StatusOK, `[
		{"id":1,"metadata":{"name":"corp"},"spec":{"type":"ldap","enabled":true,
			"ldap":{"url":"ldaps://ldap.example.org","bind_password":"leaked"},"oidc":null}},
		{"id":2,"metadata":{"name":"okta"},"spec":{"type":"oidc","enabled":true,"ldap":null,
			"oidc":{"issuer":"https://idp","client_secret":"leaked"}}}
	]`)

	rec := serveIdentitySource(t, &Dependencies{StorageAccessURL: upstream.server.URL},
		http.MethodGet, "/api/v1/identity_sources", "")

	require.Equal(t, http.StatusOK, rec.ResponseRecorder.Code)
	body := rec.ResponseRecorder.Body.String()
	assert.NotContains(t, body, "leaked")
	assert.NotContains(t, body, "bind_password")
	assert.NotContains(t, body, "client_secret")
	assert.Contains(t, body, "ldaps://ldap.example.org")
}

// A PATCH that changes the type from ldap to oidc gets an ldap sibling with a
// null bind_password from the secret backfill; the database treats an
// all-NULL sub-object as absent (dbtest covers that half).
func TestIdentitySourcePatchWithoutSecretForwardsNoSecret(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	store.EXPECT().
		GenericQuery(storage.IDENTITY_SOURCE_TABLE, "spec", mock.Anything, mock.Anything).
		Run(func(_ string, _ string, _ []storage.Filter, result interface{}) {
			resources := result.(*[]map[string]interface{}) //nolint:errcheck
			*resources = []map[string]interface{}{{
				"spec": map[string]interface{}{
					"type": "ldap",
					"ldap": map[string]interface{}{"url": "ldaps://old", "bind_password": nil},
					"oidc": nil,
				},
			}}
		}).
		Return(nil)

	upstream := newIdentitySourceUpstream(t, http.StatusNoContent, "")

	rec := serveIdentitySource(t, &Dependencies{StorageAccessURL: upstream.server.URL, Storage: store},
		http.MethodPatch, "/api/v1/identity_sources?id=eq.1", `{"spec":{"type":"oidc","enabled":true,"oidc":{
			"issuer":"https://idp.example.org","client_id":"neutree",
			"redirect_url":"https://neutree.example.org/api/v1/auth/oidc/callback",
			"allowed_redirects":["https://neutree.example.org/"]}}}`)

	assert.Equal(t, http.StatusNoContent, rec.ResponseRecorder.Code)
	require.True(t, upstream.called.Load())
	assert.Contains(t, upstream.body.Load(), `"ldap":{"bind_password":null}`)
	assert.NotContains(t, upstream.body.Load(), "client_secret")
}

func TestIdentitySourcePatchWithoutSpecPassesThrough(t *testing.T) {
	upstream := newIdentitySourceUpstream(t, http.StatusNoContent, "")

	rec := serveIdentitySource(t, &Dependencies{StorageAccessURL: upstream.server.URL},
		http.MethodPatch, "/api/v1/identity_sources?id=eq.1", `{"metadata":{"name":"corp","deletion_timestamp":"2026-01-01T00:00:00Z"}}`)

	assert.Equal(t, http.StatusNoContent, rec.ResponseRecorder.Code)
	assert.True(t, upstream.called.Load())
}

func TestIdentitySourceRejectsDelete(t *testing.T) {
	upstream := newIdentitySourceUpstream(t, http.StatusNoContent, "")

	rec := serveIdentitySource(t, &Dependencies{StorageAccessURL: upstream.server.URL},
		http.MethodDelete, "/api/v1/identity_sources?id=eq.1", "")

	assert.Equal(t, http.StatusNotFound, rec.ResponseRecorder.Code)
	assert.False(t, upstream.called.Load())
}
