package oidc_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/identity/oidc"
	"github.com/neutree-ai/neutree/pkg/identity/oidc/oidctest"
)

const (
	testClientID     = "neutree"
	testClientSecret = "client-secret"
	testRedirectURL  = "https://neutree.example.org/api/v1/auth/oidc/callback"
)

func newRP(t *testing.T, idp *oidctest.Provider) *oidc.RelyingParty {
	t.Helper()

	rp, err := oidc.New(oidc.Config{
		Issuer:       idp.Issuer(),
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		RedirectURL:  testRedirectURL,
		Scopes:       []string{"openid", "profile", "email"},
		RootCAs:      idp.CAPEM(),
		Claims:       oidc.ClaimMapping{Username: "preferred_username", DisplayName: "name", Email: "email"},
	})
	require.NoError(t, err)

	return rp
}

// login runs the code flow up to Exchange with the given nonce.
func login(t *testing.T, idp *oidctest.Provider, rp *oidc.RelyingParty, nonce string) (*oidc.Identity, error) {
	t.Helper()

	verifier := "verifier-0123456789-0123456789-0123456789-0123"

	authURL, err := rp.AuthCodeURL(context.Background(), "state-1", "nonce-1", verifier)
	require.NoError(t, err)

	code := idp.Login(t, authURL)

	return rp.Exchange(context.Background(), code, verifier, nonce)
}

func TestConfigValidate(t *testing.T) {
	valid := oidc.Config{
		Issuer:      "https://idp.example.org/realms/neutree",
		ClientID:    testClientID,
		RedirectURL: testRedirectURL,
		Scopes:      []string{"openid"},
	}
	require.NoError(t, valid.Validate())

	cases := map[string]func(c *oidc.Config){
		"issuer not a URL":      func(c *oidc.Config) { c.Issuer = "idp.example.org" },
		"redirect URL missing":  func(c *oidc.Config) { c.RedirectURL = "" },
		"client ID missing":     func(c *oidc.Config) { c.ClientID = "" },
		"no openid scope":       func(c *oidc.Config) { c.Scopes = []string{"profile"} },
		"negative timeout":      func(c *oidc.Config) { c.Timeout = -time.Second },
		"root CAs hold no cert": func(c *oidc.Config) { c.RootCAs = []byte("not pem") },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			mutate(&cfg)
			assert.ErrorIs(t, cfg.Validate(), oidc.ErrInvalidConfig)
		})
	}
}

func TestAuthCodeURL(t *testing.T) {
	idp := oidctest.New(t, testClientID, testClientSecret)

	authURL, err := newRP(t, idp).AuthCodeURL(context.Background(), "state-1", "nonce-1", "verifier-1")
	require.NoError(t, err)

	u, err := url.Parse(authURL)
	require.NoError(t, err)

	q := u.Query()
	assert.Equal(t, idp.Issuer()+"/authorize", u.Scheme+"://"+u.Host+u.Path)
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, testClientID, q.Get("client_id"))
	assert.Equal(t, testRedirectURL, q.Get("redirect_uri"))
	assert.Equal(t, "openid profile email", q.Get("scope"))
	assert.Equal(t, "state-1", q.Get("state"))
	assert.Equal(t, "nonce-1", q.Get("nonce"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.NotEmpty(t, q.Get("code_challenge"))
	assert.NotContains(t, authURL, "verifier-1")
}

func TestExchange(t *testing.T) {
	idp := oidctest.New(t, testClientID, testClientSecret)
	idp.Claims = map[string]any{"preferred_username": "alice", "name": "Alice Liddell", "email": "alice@example.org"}

	identity, err := login(t, idp, newRP(t, idp), "nonce-1")

	require.NoError(t, err)
	assert.Equal(t, &oidc.Identity{
		Issuer:      idp.Issuer(),
		Subject:     "subject-1",
		Username:    "alice",
		DisplayName: "Alice Liddell",
		Email:       "alice@example.org",
	}, identity)
}

func TestExchange_ClaimsFromUserInfo(t *testing.T) {
	idp := oidctest.New(t, testClientID, testClientSecret)
	idp.Claims = map[string]any{"preferred_username": "alice"}
	idp.UserInfoClaims = map[string]any{"preferred_username": "ignored", "name": "Alice Liddell", "email": "alice@example.org"}

	identity, err := login(t, idp, newRP(t, idp), "nonce-1")

	require.NoError(t, err)
	assert.Equal(t, "alice", identity.Username, "the ID token wins over userinfo")
	assert.Equal(t, "Alice Liddell", identity.DisplayName)
	assert.Equal(t, "alice@example.org", identity.Email)
}

func TestExchange_Rejected(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(idp *oidctest.Provider)
		nonce   string
		wantErr error
	}{
		{"nonce mismatch", func(*oidctest.Provider) {}, "another-nonce", oidc.ErrNonceMismatch},
		{"nonce replaced by the provider", func(idp *oidctest.Provider) { idp.Nonce = "forged" }, "nonce-1", oidc.ErrNonceMismatch},
		{"wrong audience", func(idp *oidctest.Provider) { idp.Audience = "another-client" }, "nonce-1", oidc.ErrInvalidIDToken},
		{"expired ID token", func(idp *oidctest.Provider) { idp.Lifetime = -time.Minute }, "nonce-1", oidc.ErrInvalidIDToken},
		{"wrong client secret", func(idp *oidctest.Provider) { idp.ClientSecret = "rotated" }, "nonce-1", oidc.ErrExchange},
		{"userinfo for another subject", func(idp *oidctest.Provider) { idp.UserInfoSubject = "subject-2" }, "nonce-1", oidc.ErrUserInfo},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := oidctest.New(t, testClientID, testClientSecret)
			rp := newRP(t, idp)
			tc.setup(idp)

			_, err := login(t, idp, rp, tc.nonce)

			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestExchange_WrongVerifier(t *testing.T) {
	idp := oidctest.New(t, testClientID, testClientSecret)
	rp := newRP(t, idp)

	authURL, err := rp.AuthCodeURL(context.Background(), "state-1", "nonce-1", "verifier-0123456789-0123456789-0123456789-0123")
	require.NoError(t, err)

	_, err = rp.Exchange(context.Background(), idp.Login(t, authURL), "verifier-of-another-login-0123456789-01234567", "nonce-1")

	assert.ErrorIs(t, err, oidc.ErrExchange)
}

func TestDiscovery_UntrustedCertificate(t *testing.T) {
	idp := oidctest.New(t, testClientID, testClientSecret)

	rp, err := oidc.New(oidc.Config{
		Issuer:      idp.Issuer(),
		ClientID:    testClientID,
		RedirectURL: testRedirectURL,
		Scopes:      []string{"openid"},
	})
	require.NoError(t, err)

	_, err = rp.AuthCodeURL(context.Background(), "s", "n", "v")

	assert.ErrorIs(t, err, oidc.ErrDiscovery)
}

func TestPing(t *testing.T) {
	t.Run("reachable", func(t *testing.T) {
		idp := oidctest.New(t, testClientID, testClientSecret)

		assert.NoError(t, newRP(t, idp).Ping(context.Background()))
	})

	t.Run("untrusted certificate", func(t *testing.T) {
		idp := oidctest.New(t, testClientID, testClientSecret)

		rp, err := oidc.New(oidc.Config{
			Issuer:      idp.Issuer(),
			ClientID:    testClientID,
			RedirectURL: testRedirectURL,
			Scopes:      []string{"openid"},
		})
		require.NoError(t, err)

		assert.ErrorIs(t, rp.Ping(context.Background()), oidc.ErrDiscovery)
	})

	t.Run("issuer mismatch", func(t *testing.T) {
		idp := oidctest.New(t, testClientID, testClientSecret)

		rp, err := oidc.New(oidc.Config{
			Issuer:      idp.Issuer() + "/realms/other",
			ClientID:    testClientID,
			RedirectURL: testRedirectURL,
			Scopes:      []string{"openid"},
			RootCAs:     idp.CAPEM(),
		})
		require.NoError(t, err)

		assert.ErrorIs(t, rp.Ping(context.Background()), oidc.ErrDiscovery)
	})

	t.Run("key set unavailable", func(t *testing.T) {
		idp := oidctest.New(t, testClientID, testClientSecret)
		idp.JWKSStatus = http.StatusInternalServerError

		assert.ErrorIs(t, newRP(t, idp).Ping(context.Background()), oidc.ErrJWKS)
	})

	t.Run("does not pin the discovery a login uses", func(t *testing.T) {
		idp := oidctest.New(t, testClientID, testClientSecret)
		rp := newRP(t, idp)

		require.NoError(t, rp.Ping(context.Background()))

		authURL, err := rp.AuthCodeURL(context.Background(), "s", "n", "verifier-0123456789-0123456789-0123456789-0123")
		require.NoError(t, err)
		assert.Contains(t, authURL, idp.Issuer())
	})
}
