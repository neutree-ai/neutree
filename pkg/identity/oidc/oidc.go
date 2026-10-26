// Package oidc logs users in with an OpenID Connect provider through the
// authorization code flow with PKCE. It keeps no per-login state: callers keep
// the state, nonce and verifier across the redirect. Returned errors never
// contain the client secret or a token.
package oidc

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// DefaultTimeout bounds each request to the provider when Config.Timeout is zero.
const DefaultTimeout = 10 * time.Second

// maxJWKSBytes bounds the key set Ping reads.
const maxJWKSBytes = 1 << 20

// ScopeOpenID must be among the requested scopes; without it the provider
// answers with plain OAuth 2.0 and no ID token.
const ScopeOpenID = gooidc.ScopeOpenID

var (
	// ErrInvalidConfig means the Config failed validation.
	ErrInvalidConfig = errors.New("oidc: invalid config")
	// ErrDiscovery means the discovery document could not be fetched or does not match the issuer.
	ErrDiscovery = errors.New("oidc: provider discovery failed")
	// ErrExchange means the authorization code could not be redeemed.
	ErrExchange = errors.New("oidc: code exchange failed")
	// ErrInvalidIDToken means the ID token is missing or fails verification.
	ErrInvalidIDToken = errors.New("oidc: invalid ID token")
	// ErrNonceMismatch means the ID token was not issued for this login.
	ErrNonceMismatch = errors.New("oidc: ID token nonce mismatch")
	// ErrUserInfo means the userinfo request failed.
	ErrUserInfo = errors.New("oidc: userinfo request failed")
	// ErrJWKS means the signing key set could not be fetched or is empty.
	ErrJWKS = errors.New("oidc: JWKS fetch failed")
)

// Config describes the provider and how its claims map to an Identity.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL must be registered with the provider.
	RedirectURL string
	// Scopes must contain ScopeOpenID.
	Scopes []string
	// RootCAs is a PEM bundle trusted in addition to the system pool.
	RootCAs []byte
	// Timeout bounds each request to the provider; zero means DefaultTimeout.
	Timeout time.Duration
	Claims  ClaimMapping
}

// ClaimMapping names the claims read into an Identity; an empty name skips it.
type ClaimMapping struct {
	Username    string
	DisplayName string
	Email       string
}

// Identity is the authenticated user. Only Issuer and Subject are stable across logins.
type Identity struct {
	Issuer      string
	Subject     string
	Username    string
	DisplayName string
	Email       string
}

// Validate reports whether the config is usable.
func (c Config) Validate() error {
	if err := validateHTTPURL(c.Issuer); err != nil {
		return fmt.Errorf("%w: issuer: %w", ErrInvalidConfig, err)
	}

	if err := validateHTTPURL(c.RedirectURL); err != nil {
		return fmt.Errorf("%w: redirect URL: %w", ErrInvalidConfig, err)
	}

	if c.ClientID == "" {
		return fmt.Errorf("%w: client ID is required", ErrInvalidConfig)
	}

	// Only confidential clients are supported.
	if strings.TrimSpace(c.ClientSecret) == "" {
		return fmt.Errorf("%w: client secret is required", ErrInvalidConfig)
	}

	if !slices.Contains(c.Scopes, ScopeOpenID) {
		return fmt.Errorf("%w: scopes must include %q", ErrInvalidConfig, ScopeOpenID)
	}

	if c.Timeout < 0 {
		return fmt.Errorf("%w: timeout must not be negative", ErrInvalidConfig)
	}

	if len(c.RootCAs) > 0 {
		if !x509.NewCertPool().AppendCertsFromPEM(c.RootCAs) {
			return fmt.Errorf("%w: root CAs hold no PEM certificate", ErrInvalidConfig)
		}
	}

	return nil
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}

	if u.Host == "" {
		return errors.New("no host")
	}

	return nil
}

// RelyingParty runs the code flow against one provider. Discovery is lazy and
// retried on failure, so startup does not depend on the provider being up.
type RelyingParty struct {
	cfg        Config
	httpClient *http.Client

	mu       sync.Mutex
	provider *gooidc.Provider
}

// New returns a RelyingParty for a validated config.
func New(cfg Config) (*RelyingParty, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}

	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck // the default transport is always an *http.Transport

	if len(cfg.RootCAs) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}

		pool.AppendCertsFromPEM(cfg.RootCAs)
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}

	return &RelyingParty{
		cfg:        cfg,
		httpClient: &http.Client{Transport: transport, Timeout: cfg.Timeout},
	}, nil
}

func (rp *RelyingParty) discover(ctx context.Context) (*gooidc.Provider, error) {
	rp.mu.Lock()
	defer rp.mu.Unlock()

	if rp.provider != nil {
		return rp.provider, nil
	}

	provider, err := gooidc.NewProvider(gooidc.ClientContext(ctx, rp.httpClient), rp.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscovery, err)
	}

	rp.provider = provider

	return provider, nil
}

// Ping fetches discovery afresh and the signing key set, to test a configuration.
// It cannot detect a wrong client secret; that only shows at a login's code exchange.
func (rp *RelyingParty) Ping(ctx context.Context) error {
	provider, err := gooidc.NewProvider(gooidc.ClientContext(ctx, rp.httpClient), rp.cfg.Issuer)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDiscovery, err)
	}

	var claims struct {
		JWKSURI string `json:"jwks_uri"`
	}

	if err := provider.Claims(&claims); err != nil || claims.JWKSURI == "" {
		return fmt.Errorf("%w: discovery document has no jwks_uri", ErrDiscovery)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claims.JWKSURI, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrJWKS, err)
	}

	resp, err := rp.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrJWKS, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s answered %s", ErrJWKS, claims.JWKSURI, resp.Status)
	}

	var keySet struct {
		Keys []json.RawMessage `json:"keys"`
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&keySet); err != nil {
		return fmt.Errorf("%w: decode %s: %w", ErrJWKS, claims.JWKSURI, err)
	}

	if len(keySet.Keys) == 0 {
		return fmt.Errorf("%w: %s holds no key", ErrJWKS, claims.JWKSURI)
	}

	return nil
}

func (rp *RelyingParty) oauth2Config(provider *gooidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     rp.cfg.ClientID,
		ClientSecret: rp.cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  rp.cfg.RedirectURL,
		Scopes:       rp.cfg.Scopes,
	}
}

// AuthCodeURL returns the provider URL to send the browser to, with the S256 challenge of verifier.
func (rp *RelyingParty) AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	provider, err := rp.discover(ctx)
	if err != nil {
		return "", err
	}

	return rp.oauth2Config(provider).AuthCodeURL(state, gooidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

// Exchange redeems the code, verifies the ID token and its nonce, and returns the
// identity. Userinfo is asked only when a mapped claim is missing from the ID
// token, as some providers keep ID tokens small.
func (rp *RelyingParty) Exchange(ctx context.Context, code, verifier, nonce string) (*Identity, error) {
	provider, err := rp.discover(ctx)
	if err != nil {
		return nil, err
	}

	ctx = gooidc.ClientContext(ctx, rp.httpClient)

	token, err := rp.oauth2Config(provider).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExchange, err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, fmt.Errorf("%w: token response has no id_token", ErrInvalidIDToken)
	}

	idToken, err := provider.Verifier(&gooidc.Config{ClientID: rp.cfg.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidIDToken, err)
	}

	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonce)) != 1 {
		return nil, ErrNonceMismatch
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: decode claims: %w", ErrInvalidIDToken, err)
	}

	if rp.missingClaims(claims) && provider.UserInfoEndpoint() != "" {
		if err := rp.mergeUserInfo(ctx, provider, token, idToken.Subject, claims); err != nil {
			return nil, err
		}
	}

	return &Identity{
		Issuer:      idToken.Issuer,
		Subject:     idToken.Subject,
		Username:    stringClaim(claims, rp.cfg.Claims.Username),
		DisplayName: stringClaim(claims, rp.cfg.Claims.DisplayName),
		Email:       stringClaim(claims, rp.cfg.Claims.Email),
	}, nil
}

func (rp *RelyingParty) missingClaims(claims map[string]any) bool {
	for _, name := range []string{rp.cfg.Claims.Username, rp.cfg.Claims.DisplayName, rp.cfg.Claims.Email} {
		if name != "" && stringClaim(claims, name) == "" {
			return true
		}
	}

	return false
}

// mergeUserInfo adds the userinfo claims that the ID token lacks. The userinfo
// response must be about the ID token's subject (OIDC Core 5.3.2).
func (rp *RelyingParty) mergeUserInfo(ctx context.Context, provider *gooidc.Provider, token *oauth2.Token, subject string, claims map[string]any) error {
	info, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUserInfo, err)
	}

	if info.Subject != subject {
		return fmt.Errorf("%w: userinfo subject %q differs from the ID token subject %q", ErrUserInfo, info.Subject, subject)
	}

	var extra map[string]any
	if err := info.Claims(&extra); err != nil {
		return fmt.Errorf("%w: decode claims: %w", ErrUserInfo, err)
	}

	for name, value := range extra {
		if _, ok := claims[name]; !ok {
			claims[name] = value
		}
	}

	return nil
}

func stringClaim(claims map[string]any, name string) string {
	if name == "" {
		return ""
	}

	value, _ := claims[name].(string)

	return value
}
