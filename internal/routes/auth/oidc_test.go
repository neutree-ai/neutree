package auth

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/supabase-community/gotrue-go/types"

	internalauth "github.com/neutree-ai/neutree/internal/auth"
	authmocks "github.com/neutree-ai/neutree/internal/auth/mocks"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/identity/oidc"
	"github.com/neutree-ai/neutree/pkg/identity/oidc/oidctest"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

const (
	testOIDCID       = "keycloak"
	testUIURL        = "http://ui.example.org/"
	testRedirectTo   = "http://ui.example.org/models?tab=all"
	testCallbackPath = "/api/v1/auth/oidc/callback"
	testCallbackURL  = "https://neutree.example.org" + testCallbackPath
)

type oidcTestEnv struct {
	idp      *oidctest.Provider
	storage  *storagemocks.MockStorage
	client   *authmocks.MockClient
	sessions *authmocks.MockSessionIssuer
	login    *OIDCLogin
	engine   *gin.Engine
}

func newOIDCTestEnv(t *testing.T) *oidcTestEnv {
	t.Helper()

	gin.SetMode(gin.TestMode)

	idp := oidctest.New(t, "neutree", "client-secret")
	idp.Claims = map[string]any{"preferred_username": "kc.alice", "name": "KC Alice", "email": "alice@example.org"}

	rp, err := oidc.New(oidc.Config{
		Issuer:       idp.Issuer(),
		ClientID:     "neutree",
		ClientSecret: "client-secret",
		RedirectURL:  testCallbackURL,
		Scopes:       []string{"openid", "profile", "email"},
		RootCAs:      idp.CAPEM(),
		Claims:       oidc.ClaimMapping{Username: "preferred_username", DisplayName: "name", Email: "email"},
	})
	require.NoError(t, err)

	login, err := NewOIDCLogin(testOIDCID, rp, testCallbackURL, []string{testUIURL, "https://other.example.org/console"}, testJWTSecret)
	require.NoError(t, err)

	e := &oidcTestEnv{
		idp:      idp,
		storage:  storagemocks.NewMockStorage(t),
		client:   authmocks.NewMockClient(t),
		sessions: authmocks.NewMockSessionIssuer(t),
		login:    login,
		engine:   gin.New(),
	}

	RegisterAuthRoutes(e.engine.Group("/api/v1"), nil, &Dependencies{
		AuthEndpoint: "http://gotrue.invalid",
		AuthConfig:   middleware.AuthConfig{JwtSecret: testJWTSecret},
		Storage:      e.storage,
		AuthClient:   e.client,
		Sessions:     e.sessions,
		OIDC:         login,
	})

	return e
}

func (e *oidcTestEnv) get(t *testing.T, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)

	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	e.engine.ServeHTTP(w, req)

	return w
}

// authorize starts a login and returns the provider URL and the state cookie.
func (e *oidcTestEnv) authorize(t *testing.T) (string, *http.Cookie) {
	t.Helper()

	w := e.get(t, "/api/v1/auth/oidc/authorize?redirect_to="+url.QueryEscape(testRedirectTo))
	require.Equal(t, http.StatusFound, w.Code)

	cookie := stateCookie(t, w)
	require.NotNil(t, cookie)

	return w.Header().Get("Location"), cookie
}

func (e *oidcTestEnv) callback(t *testing.T, query url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	return e.get(t, testCallbackPath+"?"+query.Encode(), cookies...)
}

// loginFlow runs a full login and returns the callback response.
func (e *oidcTestEnv) loginFlow(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	authURL, cookie := e.authorize(t)
	code := e.idp.Login(t, authURL)

	return e.callback(t, url.Values{"code": {code}, "state": {queryParam(t, authURL, "state")}}, cookie)
}

func (e *oidcTestEnv) externalID() string {
	return e.idp.Issuer() + "|subject-1"
}

func (e *oidcTestEnv) placeholderEmail() string {
	return oidcPlaceholderEmail(testOIDCID, e.idp.Issuer(), "subject-1")
}

// expectEmail makes GoTrue report email as the current email of userID.
func (e *oidcTestEnv) expectEmail(userID, email string) {
	e.client.EXPECT().AdminGetUser(types.AdminGetUserRequest{UserID: uuid.MustParse(userID)}).
		Return(&types.AdminGetUserResponse{User: types.User{ID: uuid.MustParse(userID), Email: email}}, nil).Once()
}

func stateCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()

	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == oidcStateCookie {
			return cookie
		}
	}

	return nil
}

func queryParam(t *testing.T, rawURL, name string) string {
	t.Helper()

	u, err := url.Parse(rawURL)
	require.NoError(t, err)

	return u.Query().Get(name)
}

// redirectFragment splits the Location of a callback response into the URL
// before the fragment and the fragment's parameters.
func redirectFragment(t *testing.T, w *httptest.ResponseRecorder) (string, url.Values) {
	t.Helper()

	require.Equal(t, http.StatusFound, w.Code)

	base, fragment, found := strings.Cut(w.Header().Get("Location"), "#")
	require.True(t, found, "no fragment in %q", w.Header().Get("Location"))

	values, err := url.ParseQuery(fragment)
	require.NoError(t, err)

	return base, values
}

func TestOIDCAuthorize(t *testing.T) {
	e := newOIDCTestEnv(t)

	authURL, cookie := e.authorize(t)

	assert.True(t, strings.HasPrefix(authURL, e.idp.Issuer()+"/authorize?"))
	assert.Equal(t, "S256", queryParam(t, authURL, "code_challenge_method"))
	assert.NotEmpty(t, queryParam(t, authURL, "code_challenge"))
	assert.NotEmpty(t, queryParam(t, authURL, "nonce"))
	assert.NotEmpty(t, queryParam(t, authURL, "state"))
	assert.Equal(t, testCallbackURL, queryParam(t, authURL, "redirect_uri"))

	assert.Equal(t, testCallbackPath, cookie.Path)
	assert.True(t, cookie.HttpOnly)
	assert.True(t, cookie.Secure, "the callback URL is https")
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	assert.Equal(t, int(oidcStateTTL/time.Second), cookie.MaxAge)

	state, err := e.login.codec.open(cookie.Value, time.Now())
	require.NoError(t, err)
	assert.Equal(t, queryParam(t, authURL, "state"), state.State)
	assert.Equal(t, queryParam(t, authURL, "nonce"), state.Nonce)
	assert.Equal(t, testRedirectTo, state.RedirectTo)
	assert.NotContains(t, authURL, state.Verifier)
	assert.NotContains(t, cookie.Value, state.Verifier, "the cookie is encrypted")
}

func TestOIDCAuthorize_RedirectAllowList(t *testing.T) {
	cases := []struct {
		redirectTo string
		allowed    bool
	}{
		{"", true},
		{"http://ui.example.org/", true},
		{"http://ui.example.org", true},
		{"http://UI.example.org/models", true},
		{"https://other.example.org/console", true},
		{"https://other.example.org/console/models", true},
		{"https://other.example.org/consoleevil", false},
		{"https://other.example.org/", false},
		{"http://other.example.org/console", false},
		{"https://ui.example.org/", false},
		{"http://ui.example.org:8080/", false},
		{"http://ui.example.org.evil.org/", false},
		{"http://evil.org/?x=http://ui.example.org/", false},
		{"//evil.org/", false},
		{"/models", false},
		{"javascript:alert(1)", false},
		{"http://user@ui.example.org/", false},
		{"http://ui.example.org/#frag", false},
		{"https://other.example.org/console/../admin", false},
		{"https://other.example.org/console/%2e%2e/admin", false},
		{`http://ui.example.org\@evil.org/`, false},
	}

	for _, tc := range cases {
		t.Run(tc.redirectTo, func(t *testing.T) {
			e := newOIDCTestEnv(t)

			w := e.get(t, "/api/v1/auth/oidc/authorize?redirect_to="+url.QueryEscape(tc.redirectTo))

			if tc.allowed {
				assert.Equal(t, http.StatusFound, w.Code)
				assert.NotNil(t, stateCookie(t, w))
			} else {
				assert.Equal(t, http.StatusBadRequest, w.Code)
				assert.Nil(t, stateCookie(t, w))
			}
		})
	}
}

func TestOIDCCallback_FirstLoginCreatesUserAndLink(t *testing.T) {
	e := newOIDCTestEnv(t)
	userID := uuid.New()

	e.storage.EXPECT().GetExternalIdentity("oidc:keycloak", e.externalID()).Return(nil, storage.ErrResourceNotFound).Once()
	e.client.EXPECT().AdminCreateUser(mock.MatchedBy(func(req types.AdminCreateUserRequest) bool {
		_, hasUsername := req.UserMetadata["username"]

		return req.Email == e.placeholderEmail() &&
			req.EmailConfirm &&
			req.Password == nil &&
			!hasUsername &&
			req.UserMetadata["preferred_username"] == "kc.alice" &&
			req.UserMetadata["name"] == "KC Alice" &&
			req.UserMetadata["email"] == "alice@example.org" &&
			req.AppMetadata["identity_source"] == "oidc"
	})).Return(&types.AdminCreateUserResponse{User: types.User{ID: userID}}, nil).Once()
	e.storage.EXPECT().CreateExternalIdentity(&storage.ExternalIdentity{
		Source: "oidc:keycloak", ExternalID: e.externalID(), UserID: userID.String(),
	}).Return(nil).Once()
	e.expectEmail(userID.String(), e.placeholderEmail())
	e.sessions.EXPECT().GenerateMagicLink(mock.Anything, e.placeholderEmail()).
		Return(&internalauth.MagicLink{UserID: userID.String(), HashedToken: "hashed-token"}, nil).Once()

	w := e.loginFlow(t)

	base, fragment := redirectFragment(t, w)
	assert.Equal(t, testRedirectTo, base)
	assert.Equal(t, url.Values{"token_hash": {"hashed-token"}, "type": {"magiclink"}}, fragment)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	cleared := stateCookie(t, w)
	require.NotNil(t, cleared)
	assert.Equal(t, testCallbackPath, cleared.Path)
	assert.Negative(t, cleared.MaxAge)

	e.sessions.AssertNotCalled(t, "VerifyMagicLink", mock.Anything, mock.Anything)
}

func TestOIDCCallback_LaterLoginUsesLink(t *testing.T) {
	e := newOIDCTestEnv(t)
	userID := uuid.NewString()

	e.storage.EXPECT().GetExternalIdentity("oidc:keycloak", e.externalID()).
		Return(&storage.ExternalIdentity{Source: "oidc:keycloak", ExternalID: e.externalID(), UserID: userID}, nil).Once()
	e.expectEmail(userID, e.placeholderEmail())
	e.sessions.EXPECT().GenerateMagicLink(mock.Anything, e.placeholderEmail()).
		Return(&internalauth.MagicLink{UserID: userID, HashedToken: "hashed-token"}, nil).Once()

	_, fragment := redirectFragment(t, e.loginFlow(t))

	assert.Equal(t, "hashed-token", fragment.Get("token_hash"))
	e.client.AssertNotCalled(t, "AdminCreateUser", mock.Anything)
}

func TestOIDCCallback_LoginAfterEmailChangedInGoTrue(t *testing.T) {
	e := newOIDCTestEnv(t)
	userID := uuid.NewString()

	e.storage.EXPECT().GetExternalIdentity("oidc:keycloak", e.externalID()).
		Return(&storage.ExternalIdentity{UserID: userID}, nil).Once()
	e.expectEmail(userID, "renamed@example.org")
	e.sessions.EXPECT().GenerateMagicLink(mock.Anything, "renamed@example.org").
		Return(&internalauth.MagicLink{UserID: userID, HashedToken: "hashed-token"}, nil).Once()

	_, fragment := redirectFragment(t, e.loginFlow(t))

	assert.Equal(t, url.Values{"token_hash": {"hashed-token"}, "type": {"magiclink"}}, fragment)
	e.client.AssertNotCalled(t, "AdminCreateUser", mock.Anything)
}

func TestOIDCCallback_GetUserFailsClosed(t *testing.T) {
	cases := map[string]struct {
		user *types.AdminGetUserResponse
		err  error
	}{
		"error":    {nil, errors.New("gotrue down")},
		"no email": {&types.AdminGetUserResponse{}, nil},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newOIDCTestEnv(t)
			userID := uuid.New()

			e.storage.EXPECT().GetExternalIdentity("oidc:keycloak", e.externalID()).
				Return(&storage.ExternalIdentity{UserID: userID.String()}, nil).Once()
			e.client.EXPECT().AdminGetUser(types.AdminGetUserRequest{UserID: userID}).Return(tc.user, tc.err).Once()

			_, fragment := redirectFragment(t, e.loginFlow(t))

			assert.Equal(t, url.Values{"error": {oidcErrServerError}}, fragment)
			e.sessions.AssertNotCalled(t, "GenerateMagicLink", mock.Anything, mock.Anything)
		})
	}
}

func TestOIDCCallback_MagicLinkForAnotherUserFailsClosed(t *testing.T) {
	e := newOIDCTestEnv(t)
	userID := uuid.NewString()

	e.storage.EXPECT().GetExternalIdentity("oidc:keycloak", e.externalID()).
		Return(&storage.ExternalIdentity{UserID: userID}, nil).Once()
	e.expectEmail(userID, e.placeholderEmail())
	e.sessions.EXPECT().GenerateMagicLink(mock.Anything, e.placeholderEmail()).
		Return(&internalauth.MagicLink{UserID: uuid.NewString(), HashedToken: "hashed-token"}, nil).Once()

	w := e.loginFlow(t)

	base, fragment := redirectFragment(t, w)
	assert.Equal(t, testRedirectTo, base)
	assert.Equal(t, url.Values{"error": {oidcErrServerError}}, fragment)
	assert.NotContains(t, w.Header().Get("Location"), "hashed-token")
}

// TestOIDCCallback_Rejected covers callbacks that fail before any user lookup;
// the storage mock fails the test if one is attempted.
func TestOIDCCallback_Rejected(t *testing.T) {
	cases := []struct {
		name string
		// callback sends the callback for a login started with authURL and cookie.
		callback     func(t *testing.T, e *oidcTestEnv, authURL string, cookie *http.Cookie) *httptest.ResponseRecorder
		wantBase     string
		wantErrorKey string
	}{
		{
			name: "missing state cookie",
			callback: func(t *testing.T, e *oidcTestEnv, authURL string, _ *http.Cookie) *httptest.ResponseRecorder {
				return e.callback(t, url.Values{"code": {e.idp.Login(t, authURL)}, "state": {queryParam(t, authURL, "state")}})
			},
			wantBase:     testUIURL,
			wantErrorKey: oidcErrInvalidState,
		},
		{
			name: "state mismatch",
			callback: func(t *testing.T, e *oidcTestEnv, authURL string, cookie *http.Cookie) *httptest.ResponseRecorder {
				return e.callback(t, url.Values{"code": {e.idp.Login(t, authURL)}, "state": {"tampered"}}, cookie)
			},
			wantBase:     testRedirectTo,
			wantErrorKey: oidcErrInvalidState,
		},
		{
			name: "tampered cookie",
			callback: func(t *testing.T, e *oidcTestEnv, authURL string, cookie *http.Cookie) *httptest.ResponseRecorder {
				forged := *cookie
				forged.Value = cookie.Value[:len(cookie.Value)-4] + "AAAA"

				return e.callback(t, url.Values{"code": {e.idp.Login(t, authURL)}, "state": {queryParam(t, authURL, "state")}}, &forged)
			},
			wantBase:     testUIURL,
			wantErrorKey: oidcErrInvalidState,
		},
		{
			name: "expired cookie",
			callback: func(t *testing.T, e *oidcTestEnv, authURL string, cookie *http.Cookie) *httptest.ResponseRecorder {
				e.login.now = func() time.Time { return time.Now().Add(oidcStateTTL + time.Minute) }
				return e.callback(t, url.Values{"code": {e.idp.Login(t, authURL)}, "state": {queryParam(t, authURL, "state")}}, cookie)
			},
			wantBase:     testUIURL,
			wantErrorKey: oidcErrInvalidState,
		},
		{
			name: "provider error",
			callback: func(t *testing.T, e *oidcTestEnv, authURL string, cookie *http.Cookie) *httptest.ResponseRecorder {
				return e.callback(t, url.Values{"error": {"access_denied"}, "state": {queryParam(t, authURL, "state")}}, cookie)
			},
			wantBase:     testRedirectTo,
			wantErrorKey: oidcErrIdPError,
		},
		{
			name: "no code",
			callback: func(t *testing.T, e *oidcTestEnv, authURL string, cookie *http.Cookie) *httptest.ResponseRecorder {
				return e.callback(t, url.Values{"state": {queryParam(t, authURL, "state")}}, cookie)
			},
			wantBase:     testRedirectTo,
			wantErrorKey: oidcErrLoginFailed,
		},
		{
			name: "nonce mismatch",
			callback: func(t *testing.T, e *oidcTestEnv, authURL string, cookie *http.Cookie) *httptest.ResponseRecorder {
				e.idp.Nonce = "nonce-of-another-login"
				return e.callback(t, url.Values{"code": {e.idp.Login(t, authURL)}, "state": {queryParam(t, authURL, "state")}}, cookie)
			},
			wantBase:     testRedirectTo,
			wantErrorKey: oidcErrLoginFailed,
		},
		{
			name: "wrong audience",
			callback: func(t *testing.T, e *oidcTestEnv, authURL string, cookie *http.Cookie) *httptest.ResponseRecorder {
				e.idp.Audience = "another-client"
				return e.callback(t, url.Values{"code": {e.idp.Login(t, authURL)}, "state": {queryParam(t, authURL, "state")}}, cookie)
			},
			wantBase:     testRedirectTo,
			wantErrorKey: oidcErrLoginFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newOIDCTestEnv(t)
			authURL, cookie := e.authorize(t)

			w := tc.callback(t, e, authURL, cookie)

			base, fragment := redirectFragment(t, w)
			assert.Equal(t, tc.wantBase, base)
			assert.Equal(t, url.Values{"error": {tc.wantErrorKey}}, fragment)
		})
	}
}

func TestOIDCStateCodec(t *testing.T) {
	codec, err := newStateCodec(testJWTSecret, testOIDCID)
	require.NoError(t, err)

	state := &oidcLoginState{State: "s", Nonce: "n", Verifier: "v", RedirectTo: testUIURL, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	sealed, err := codec.seal(state)
	require.NoError(t, err)

	opened, err := codec.open(sealed, time.Now())
	require.NoError(t, err)
	assert.Equal(t, state, opened)

	_, err = codec.open(sealed, time.Now().Add(2*time.Minute))
	assert.ErrorIs(t, err, errInvalidLoginState, "expired")

	otherSecret, err := newStateCodec("another-secret", testOIDCID)
	require.NoError(t, err)
	_, err = otherSecret.open(sealed, time.Now())
	assert.ErrorIs(t, err, errInvalidLoginState, "another key")

	otherProvider, err := newStateCodec(testJWTSecret, "another-idp")
	require.NoError(t, err)
	_, err = otherProvider.open(sealed, time.Now())
	assert.ErrorIs(t, err, errInvalidLoginState, "another provider")

	_, err = newStateCodec("", testOIDCID)
	assert.Error(t, err)
}

func TestOIDCPlaceholderEmail(t *testing.T) {
	assert.Equal(t, "3527d1c17be1d48c8f7009478fe1e55c@keycloak.oidc.neutree.local",
		oidcPlaceholderEmail("keycloak", "https://idp.example.org/realms/neutree", "248289761001"))
}

func TestNewOIDCLogin_Invalid(t *testing.T) {
	cases := map[string]struct {
		id, callback string
		allowed      []string
	}{
		"bad id":                {"Key Cloak", testCallbackURL, []string{testUIURL}},
		"no allowed redirects":  {testOIDCID, testCallbackURL, nil},
		"relative redirect":     {testOIDCID, testCallbackURL, []string{"/ui/"}},
		"redirect with query":   {testOIDCID, testCallbackURL, []string{testUIURL + "?a=b"}},
		"callback without path": {testOIDCID, "https://neutree.example.org", []string{testUIURL}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewOIDCLogin(tc.id, nil, tc.callback, tc.allowed, testJWTSecret)
			assert.Error(t, err)
		})
	}
}

func TestOIDCRoutes_Registration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, path := range []string{"/api/v1/auth/oidc/authorize", "/api/v1/auth/oidc/callback"} {
		t.Run(path, func(t *testing.T) {
			engine := gin.New()
			RegisterAuthRoutes(engine.Group("/api/v1"), nil, &Dependencies{AuthEndpoint: "http://gotrue.invalid"})

			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))

			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	}
}

func TestVerifyProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath, gotBody string

	gotrue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotPath, gotBody = r.Method+" "+r.URL.Path, string(body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testSession))
	}))
	t.Cleanup(gotrue.Close)

	engine := gin.New()
	RegisterAuthRoutes(engine.Group("/api/v1"), nil, &Dependencies{AuthEndpoint: gotrue.URL})

	verify := func(body string) *closeNotifyRecorder {
		w := &closeNotifyRecorder{httptest.NewRecorder()}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/verify", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(w, req)

		return w
	}

	t.Run("magiclink is proxied intact", func(t *testing.T) {
		body := `{"token_hash":"hashed-token","type":"magiclink"}`
		w := verify(body)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, testSession, w.Body.String())
		assert.Equal(t, "POST /verify", gotPath)
		assert.Equal(t, body, gotBody)
	})

	for _, body := range []string{
		`{"token_hash":"hashed-token","type":"signup"}`,
		`{"token_hash":"hashed-token","type":"recovery"}`,
		`{"token_hash":"hashed-token","type":"invite"}`,
		`{"token_hash":"hashed-token","type":"email_change"}`,
		`{"token_hash":"hashed-token"}`,
		`not json`,
	} {
		t.Run(body, func(t *testing.T) {
			gotPath = ""
			w := verify(body)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Empty(t, gotPath, "request reached GoTrue")
		})
	}
}
