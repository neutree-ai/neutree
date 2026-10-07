// Package oidctest runs a minimal OpenID Connect provider for tests: discovery,
// JWKS, a token endpoint that checks the client secret and the PKCE verifier,
// and userinfo. It serves over TLS with its own certificate, so the client
// under test has to trust CAPEM.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

const keyID = "test-key"

// Provider is a fake OpenID Connect provider. Change its exported fields
// before a login to shape the tokens it issues.
type Provider struct {
	Server       *httptest.Server
	ClientID     string
	ClientSecret string

	// Subject is the sub claim of the ID token and of userinfo.
	Subject string
	// Claims are added to the ID token.
	Claims map[string]any
	// UserInfoClaims are served by userinfo next to sub.
	UserInfoClaims map[string]any
	// UserInfoSubject overrides sub in userinfo when set.
	UserInfoSubject string
	// Audience overrides the ID token aud, which is ClientID by default.
	Audience string
	// Nonce overrides the ID token nonce, which is the authorize request's by default.
	Nonce string
	// Lifetime is how long the ID token is valid; negative issues an expired token.
	Lifetime time.Duration

	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]authRequest
}

type authRequest struct {
	nonce       string
	challenge   string
	redirectURI string
}

// New starts a provider that is closed when the test ends.
func New(t testing.TB, clientID, clientSecret string) *Provider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	p := &Provider{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Subject:      "subject-1",
		Lifetime:     time.Hour,
		key:          key,
		codes:        map[string]authRequest{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("/jwks", p.jwks)
	mux.HandleFunc("/token", p.token)
	mux.HandleFunc("/userinfo", p.userinfo)

	p.Server = httptest.NewTLSServer(mux)
	t.Cleanup(p.Server.Close)

	return p
}

// Issuer is the provider's issuer URL.
func (p *Provider) Issuer() string {
	return p.Server.URL
}

// CAPEM is the certificate the provider serves, as PEM.
func (p *Provider) CAPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.Server.Certificate().Raw})
}

// Login plays the user signing in at the authorization URL: it records the
// request's nonce and PKCE challenge and returns the code the provider would
// send to the callback.
func (p *Provider) Login(t testing.TB, authURL string) string {
	t.Helper()

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}

	q := u.Query()
	if q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("unexpected authorization request %s", authURL)
	}

	code := randomString(t)

	p.mu.Lock()
	p.codes[code] = authRequest{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri")}
	p.mu.Unlock()

	return code
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.Issuer(),
		"authorization_endpoint":                p.Issuer() + "/authorize",
		"token_endpoint":                        p.Issuer() + "/token",
		"jwks_uri":                              p.Issuer() + "/jwks",
		"userinfo_endpoint":                     p.Issuer() + "/userinfo",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &p.key.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig"},
	}})
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}

	if id != p.ClientID || secret != p.ClientSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}

	code := r.PostForm.Get("code")

	p.mu.Lock()
	req, found := p.codes[code]
	delete(p.codes, code)
	p.mu.Unlock()

	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != req.redirectURI ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != req.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}

	idToken, err := p.signIDToken(req.nonce)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": "access-" + code,
		"token_type":   "Bearer",
		"expires_in":   300,
		"id_token":     idToken,
	})
}

func (p *Provider) signIDToken(nonce string) (string, error) {
	now := time.Now()
	claims := map[string]any{
		"iss":   p.Issuer(),
		"sub":   p.Subject,
		"aud":   p.ClientID,
		"iat":   now.Unix(),
		"exp":   now.Add(p.Lifetime).Unix(),
		"nonce": nonce,
	}

	if p.Audience != "" {
		claims["aud"] = p.Audience
	}

	if p.Nonce != "" {
		claims["nonce"] = p.Nonce
	}

	maps.Copy(claims, p.Claims)

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID))
	if err != nil {
		return "", err
	}

	signed, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}

	return signed.CompactSerialize()
}

func (p *Provider) userinfo(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	claims := map[string]any{"sub": p.Subject}
	if p.UserInfoSubject != "" {
		claims["sub"] = p.UserInfoSubject
	}

	maps.Copy(claims, p.UserInfoClaims)
	writeJSON(w, http.StatusOK, claims)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func randomString(t testing.TB) string {
	t.Helper()

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}

	return base64.RawURLEncoding.EncodeToString(b)
}
