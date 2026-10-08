package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func serveLoginIdentitySources(t *testing.T, store *storagemocks.MockStorage) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)

	router := gin.New()
	RegisterAuthRoutes(router.Group("/api/v1"), nil, &Dependencies{Storage: store})

	// No Authorization header: the login page has no session yet.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/identity-sources", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func TestListLoginIdentitySources(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.EXPECT().ListLoginIdentitySources().Return([]v1.LoginIdentitySource{
		{Name: "corp", DisplayName: "Corp LDAP", Type: v1.IdentitySourceTypeLDAP},
		{Name: "okta", DisplayName: "Okta", Type: v1.IdentitySourceTypeOIDC},
	}, nil)

	rec := serveLoginIdentitySources(t, store)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.JSONEq(t, `[
		{"name":"corp","display_name":"Corp LDAP","type":"ldap"},
		{"name":"okta","display_name":"Okta","type":"oidc"}
	]`, rec.Body.String())
}

func TestListLoginIdentitySourcesEmpty(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.EXPECT().ListLoginIdentitySources().Return([]v1.LoginIdentitySource{}, nil)

	rec := serveLoginIdentitySources(t, store)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `[]`, rec.Body.String())
}

func TestListLoginIdentitySourcesStorageError(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.EXPECT().ListLoginIdentitySources().Return(nil, errors.New("connection refused to 10.0.0.1"))

	rec := serveLoginIdentitySources(t, store)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.NotContains(t, rec.Body.String(), "10.0.0.1")
}
