package auth

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/identity/oidc/oidctest"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestOIDCAuthorize_UnknownSource(t *testing.T) {
	e := newOIDCTestEnv(t)
	e.storage.EXPECT().ListIdentitySource(matchSourceName("nope")).Return(nil, nil).Once()

	w := e.get(t, "/api/v1/auth/oidc/authorize?source=nope&redirect_to="+url.QueryEscape(testRedirectTo))

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, msgSourceNotFound, errorBody(t, w))
	assert.Nil(t, stateCookie(t, w))
	assert.Empty(t, w.Header().Get("Location"), "an unvalidated redirect_to is never followed")
}

func TestOIDCAuthorize_SourceOmittedWithOneEnabled(t *testing.T) {
	e := newOIDCTestEnv(t)
	e.storage.EXPECT().ListLoginIdentitySources().Return([]v1.LoginIdentitySource{
		{Name: "corp-ldap", Type: v1.IdentitySourceTypeLDAP},
		{Name: testOIDCID, Type: v1.IdentitySourceTypeOIDC},
	}, nil).Once()

	w := e.get(t, "/api/v1/auth/oidc/authorize?redirect_to="+url.QueryEscape(testRedirectTo))

	require.Equal(t, http.StatusFound, w.Code)
	assert.True(t, strings.HasPrefix(stateCookie(t, w).Value, testOIDCID+"."))
}

// addSource registers a second OIDC source, served by its own provider.
func (e *oidcTestEnv) addSource(t *testing.T, name string) *oidctest.Provider {
	t.Helper()

	idp := oidctest.New(t, "neutree", "client-secret")
	idp.Subject = "subject-of-" + name
	expectSource(e.storage, oidcSourceFor(name, idp, true), &storage.IdentitySourceSecrets{OIDCClientSecret: "client-secret"})

	return idp
}

// A state cookie sealed for one source, relabelled for another, does not open.
func TestOIDCCallback_CookieRelabelledForAnotherSource(t *testing.T) {
	e := newOIDCTestEnv(t)
	e.addSource(t, "other-idp")

	authURL, cookie := e.authorize(t)
	_, ciphertext, _ := strings.Cut(cookie.Value, ".")

	forged := *cookie
	forged.Value = "other-idp." + ciphertext

	w := e.callback(t, url.Values{"code": {e.idp.Login(t, authURL)}, "state": {queryParam(t, authURL, "state")}}, &forged)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, oidcErrInvalidState, errorBody(t, w))
	e.storage.AssertNotCalled(t, "GetExternalIdentity", mock.Anything, mock.Anything)
}

// A login started with one source finishes with that source only: a code
// issued by another source's provider is redeemed at the first provider,
// which refuses it.
func TestOIDCCallback_CodeOfAnotherSourceFails(t *testing.T) {
	e := newOIDCTestEnv(t)
	other := e.addSource(t, "other-idp")

	otherAuth := e.get(t, "/api/v1/auth/oidc/authorize?source=other-idp&redirect_to="+url.QueryEscape(testRedirectTo))
	require.Equal(t, http.StatusFound, otherAuth.Code)

	otherCode := other.Login(t, otherAuth.Header().Get("Location"))

	authURL, cookie := e.authorize(t)

	w := e.callback(t, url.Values{"code": {otherCode}, "state": {queryParam(t, authURL, "state")}}, cookie)

	_, fragment := redirectFragment(t, w)
	assert.Equal(t, url.Values{"error": {oidcErrLoginFailed}}, fragment)
	e.storage.AssertNotCalled(t, "GetExternalIdentity", mock.Anything, mock.Anything)
}

// A source disabled while its user is at the provider does not finish the login.
func TestOIDCCallback_SourceDisabledMeanwhile(t *testing.T) {
	e := newOIDCTestEnv(t)

	authURL, cookie := e.authorize(t)

	// The cached entry expires and the next read finds the source disabled.
	e.sources.mu.Lock()
	e.sources.entries = map[string]*loginSource{}
	e.sources.mu.Unlock()

	e.storage.ExpectedCalls = nil
	e.storage.EXPECT().ListIdentitySource(matchSourceName(testOIDCID)).
		Return([]v1.IdentitySource{oidcSourceFor(testOIDCID, e.idp, false)}, nil).Once()

	w := e.callback(t, url.Values{"code": {e.idp.Login(t, authURL)}, "state": {queryParam(t, authURL, "state")}}, cookie)

	base, fragment := redirectFragment(t, w)
	assert.Equal(t, testRedirectTo, base)
	assert.Equal(t, url.Values{"error": {oidcErrSourceUnavailable}}, fragment)
	e.storage.AssertNotCalled(t, "GetExternalIdentity", mock.Anything, mock.Anything)
}

func TestOIDCPlaceholderEmail_PerSource(t *testing.T) {
	a := oidcPlaceholderEmail("keycloak", "https://idp.example.org", "sub")
	b := oidcPlaceholderEmail("corp-sso", "https://idp.example.org", "sub")

	assert.True(t, strings.HasSuffix(a, "@keycloak.oidc.neutree.local"))
	assert.True(t, strings.HasSuffix(b, "@corp-sso.oidc.neutree.local"))
	assert.Equal(t, strings.Split(a, "@")[0], strings.Split(b, "@")[0])
}
