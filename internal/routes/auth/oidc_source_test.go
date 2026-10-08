package auth

import (
	"errors"
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
	other := oidcSourceFor("other-idp", oidctest.New(t, "neutree", "client-secret"), true)
	other.Spec.OIDC.AllowedRedirects = []string{"https://other.example.org/console", testUIURL}
	expectSource(e.storage, other, &storage.IdentitySourceSecrets{OIDCClientSecret: "client-secret"})

	authURL, cookie := e.authorize(t)
	_, ciphertext, _ := strings.Cut(cookie.Value, ".")

	forged := *cookie
	forged.Value = "other-idp." + ciphertext

	w := e.callback(t, url.Values{"code": {e.idp.Login(t, authURL)}, "state": {queryParam(t, authURL, "state")}}, &forged)

	base, fragment := redirectFragment(t, w)
	assert.Equal(t, "https://other.example.org/console/", base, "the fallback of the source the cookie names")
	assert.Equal(t, url.Values{"error": {oidcErrInvalidState}}, fragment)
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

// A callback whose state cookie cannot be read redirects with invalid_state to
// a fallback that is never taken from the request: an allowed redirect of the
// source the cookie names, else of the only enabled OIDC source, else "/".
func TestOIDCCallback_UnreadableStateFallback(t *testing.T) {
	oneOIDC := []v1.LoginIdentitySource{
		{Name: "corp-ldap", Type: v1.IdentitySourceTypeLDAP},
		{Name: testOIDCID, Type: v1.IdentitySourceTypeOIDC},
	}
	twoOIDC := append(oneOIDC, v1.LoginIdentitySource{Name: "other-idp", Type: v1.IdentitySourceTypeOIDC})

	cases := []struct {
		name     string
		cookie   string // "" sends no cookie
		setup    func(e *oidcTestEnv)
		wantBase string
	}{
		{
			name: "no cookie, one enabled OIDC source",
			setup: func(e *oidcTestEnv) {
				e.storage.EXPECT().ListLoginIdentitySources().Return(oneOIDC, nil).Once()
			},
			wantBase: testUIURL,
		},
		{
			name: "no cookie, several enabled OIDC sources",
			setup: func(e *oidcTestEnv) {
				e.storage.EXPECT().ListLoginIdentitySources().Return(twoOIDC, nil).Once()
			},
			wantBase: "/",
		},
		{
			name: "no cookie, no enabled OIDC source",
			setup: func(e *oidcTestEnv) {
				e.storage.EXPECT().ListLoginIdentitySources().Return(oneOIDC[:1], nil).Once()
			},
			wantBase: "/",
		},
		{
			name: "no cookie, listing sources fails",
			setup: func(e *oidcTestEnv) {
				e.storage.EXPECT().ListLoginIdentitySources().Return(nil, errors.New("db down")).Once()
			},
			wantBase: "/",
		},
		{
			name:     "garbage cookie naming the source",
			cookie:   testOIDCID + ".garbage",
			wantBase: testUIURL,
		},
		{
			name:   "cookie naming an unknown source",
			cookie: "nope.garbage",
			setup: func(e *oidcTestEnv) {
				e.storage.EXPECT().ListIdentitySource(matchSourceName("nope")).Return(nil, nil).Once()
				e.storage.EXPECT().ListLoginIdentitySources().Return(twoOIDC, nil).Once()
			},
			wantBase: "/",
		},
		{
			name:   "cookie naming an LDAP source",
			cookie: "corp-ldap.garbage",
			setup: func(e *oidcTestEnv) {
				expectSource(e.storage, ldapSource("corp-ldap", true), &storage.IdentitySourceSecrets{})
				e.storage.EXPECT().ListLoginIdentitySources().Return(oneOIDC, nil).Once()
			},
			wantBase: testUIURL,
		},
		{
			name:   "cookie with an invalid source name",
			cookie: "../evil.example.org.garbage",
			setup: func(e *oidcTestEnv) {
				e.storage.EXPECT().ListLoginIdentitySources().Return(twoOIDC, nil).Once()
			},
			wantBase: "/",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newOIDCTestEnv(t)
			if tc.setup != nil {
				tc.setup(e)
			}

			var cookies []*http.Cookie
			if tc.cookie != "" {
				cookies = append(cookies, &http.Cookie{Name: oidcStateCookie, Value: tc.cookie})
			}

			w := e.callback(t, url.Values{"code": {"code"}, "state": {"https://evil.example.org/"}}, cookies...)

			base, fragment := redirectFragment(t, w)
			assert.Equal(t, tc.wantBase, base)
			assert.Equal(t, url.Values{"error": {oidcErrInvalidState}}, fragment)
		})
	}
}
