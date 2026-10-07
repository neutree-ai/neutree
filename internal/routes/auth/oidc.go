package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/identity/oidc"
)

// oidcEmailDomain is the parent domain of OIDC users' placeholder emails; each
// provider gets the subdomain <provider id>.oidc.neutree.local.
const oidcEmailDomain = "oidc.neutree.local"

// Error codes the callback puts in the redirect fragment as error=<code>. They
// are deliberately coarse; the details are logged.
const (
	oidcErrInvalidState   = "invalid_state"
	oidcErrIdPError       = "idp_error"
	oidcErrIdPUnavailable = "idp_unavailable"
	oidcErrLoginFailed    = "login_failed"
	oidcErrServerError    = "server_error"
)

// OIDCRelyingParty runs the code flow with the provider; *oidc.RelyingParty
// implements it.
type OIDCRelyingParty interface {
	AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error)
	Exchange(ctx context.Context, code, verifier, nonce string) (*oidc.Identity, error)
}

// OIDCLogin is the OpenID Connect provider users log in with.
type OIDCLogin struct {
	id    string
	rp    OIDCRelyingParty
	codec *stateCodec
	// allowedRedirects are the UI locations a login may return to; the first
	// one is the default.
	allowedRedirects []*url.URL
	cookiePath       string
	cookieSecure     bool
	now              func() time.Time
}

// NewOIDCLogin checks the login settings. id names the provider in links and in
// the placeholder emails of new users; keep it stable, since users linked under
// another id are not found again. callbackURL is the public URL of
// the callback route, registered with the provider. allowedRedirects are
// absolute URLs; a login may return to any URL under one of them. stateSecret
// keys the login state cookie.
func NewOIDCLogin(id string, rp OIDCRelyingParty, callbackURL string, allowedRedirects []string, stateSecret string) (*OIDCLogin, error) {
	if !auth.ValidOIDCProviderID(id) {
		return nil, fmt.Errorf("invalid OIDC provider ID %q", id)
	}

	callback, err := url.Parse(callbackURL)
	if err != nil || callback.Path == "" {
		return nil, fmt.Errorf("invalid OIDC callback URL %q", callbackURL)
	}

	login := &OIDCLogin{
		id:           id,
		rp:           rp,
		cookiePath:   callback.Path,
		cookieSecure: callback.Scheme == "https",
		now:          time.Now,
	}

	if login.allowedRedirects, err = ParseOIDCAllowedRedirects(allowedRedirects); err != nil {
		return nil, err
	}

	if login.codec, err = newStateCodec(stateSecret, id); err != nil {
		return nil, err
	}

	return login, nil
}

// ParseOIDCAllowedRedirects parses the UI locations an OIDC login may return
// to. Each must be an absolute http(s) URL without query or fragment; its path
// is a prefix, so it gets a trailing '/'.
func ParseOIDCAllowedRedirects(raws []string) ([]*url.URL, error) {
	if len(raws) == 0 {
		return nil, errors.New("at least one allowed OIDC redirect is required")
	}

	allowed := make([]*url.URL, 0, len(raws))

	for _, raw := range raws {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("allowed OIDC redirect %q must be an absolute http(s) URL without user, query or fragment", raw)
		}

		if !strings.HasSuffix(u.Path, "/") {
			u.Path += "/"
		}

		allowed = append(allowed, u)
	}

	return allowed, nil
}

// handleOIDCAuthorize starts a login: it remembers the login in the state
// cookie and sends the browser to the provider.
func handleOIDCAuthorize(o *OIDCLogin) gin.HandlerFunc {
	return func(c *gin.Context) {
		redirectTo, ok := o.allowedRedirect(c.Query("redirect_to"))
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "redirect_to is not an allowed redirect target"})
			return
		}

		c.Header("Cache-Control", "no-store")

		state, err := o.newLoginState(redirectTo)
		if err != nil {
			klog.Errorf("OIDC login via %s: new login state: %v", o.id, err)
			o.redirectWithError(c, redirectTo, oidcErrServerError)

			return
		}

		authURL, err := o.rp.AuthCodeURL(c.Request.Context(), state.State, state.Nonce, state.Verifier)
		if err != nil {
			klog.Errorf("OIDC login via %s: %v", o.id, err)
			o.redirectWithError(c, redirectTo, oidcErrIdPUnavailable)

			return
		}

		sealed, err := o.codec.seal(state)
		if err != nil {
			klog.Errorf("OIDC login via %s: seal login state: %v", o.id, err)
			o.redirectWithError(c, redirectTo, oidcErrServerError)

			return
		}

		o.setStateCookie(c, sealed, int(oidcStateTTL/time.Second))
		c.Redirect(http.StatusFound, authURL)
	}
}

// handleOIDCCallback finishes a login. The provider's account is linked to a
// GoTrue user like an LDAP account, and the browser goes back to the UI with a
// one-time magic link token in the URL fragment, which the UI exchanges on
// /auth/verify for a session. The fragment never reaches a server, so the
// token stays out of access logs and Referer headers.
func handleOIDCCallback(deps *Dependencies) gin.HandlerFunc {
	o := deps.OIDC

	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")

		state, err := o.takeLoginState(c)
		if err != nil {
			klog.Infof("OIDC callback of %s rejected: %v", o.id, err)
			o.redirectWithError(c, o.allowedRedirects[0].String(), oidcErrInvalidState)

			return
		}

		if subtle.ConstantTimeCompare([]byte(c.Query("state")), []byte(state.State)) != 1 {
			klog.Infof("OIDC callback of %s rejected: state does not match the login state cookie", o.id)
			o.redirectWithError(c, state.RedirectTo, oidcErrInvalidState)

			return
		}

		if idpErr := c.Query("error"); idpErr != "" {
			klog.Infof("OIDC login via %s refused by the provider: %s: %s", o.id, idpErr, c.Query("error_description"))
			o.redirectWithError(c, state.RedirectTo, oidcErrIdPError)

			return
		}

		fragment, code := o.finishLogin(c.Request.Context(), deps, c.Query("code"), state)
		if code != "" {
			o.redirectWithError(c, state.RedirectTo, code)
			return
		}

		c.Redirect(http.StatusFound, state.RedirectTo+"#"+fragment)
	}
}

// finishLogin redeems the code and returns the redirect fragment carrying the
// magic link token, or an error code.
func (o *OIDCLogin) finishLogin(ctx context.Context, deps *Dependencies, code string, state *oidcLoginState) (string, string) {
	if code == "" {
		klog.Infof("OIDC callback of %s rejected: no code", o.id)
		return "", oidcErrLoginFailed
	}

	identity, err := o.rp.Exchange(ctx, code, state.Verifier, state.Nonce)
	if err != nil {
		klog.Warningf("OIDC login via %s failed: %v", o.id, err)

		if errors.Is(err, oidc.ErrDiscovery) {
			return "", oidcErrIdPUnavailable
		}

		return "", oidcErrLoginFailed
	}

	if identity.Issuer == "" || identity.Subject == "" {
		klog.Warningf("OIDC login via %s failed: ID token has no issuer or subject", o.id)
		return "", oidcErrLoginFailed
	}

	logName := identity.Username
	if logName == "" {
		logName = identity.Subject
	}

	userID, err := ensureExternalUser(ctx, deps, externalAccount{
		Source:         auth.OIDCLinkSource(o.id),
		ExternalID:     oidcExternalID(identity.Issuer, identity.Subject),
		IdentitySource: auth.OIDCSource,
		Email:          oidcPlaceholderEmail(o.id, identity.Issuer, identity.Subject),
		Metadata:       externalUserMetadata(identity.Username, identity.DisplayName, identity.Email),
		LogName:        logName,
	})
	if err != nil {
		klog.Errorf("OIDC login of %q via %s: link user: %v", logName, o.id, err)
		return "", oidcErrServerError
	}

	link, err := magicLinkFor(ctx, deps, userID)
	if err != nil {
		klog.Errorf("OIDC login of %q via %s (user %s): %v", logName, o.id, userID, err)
		return "", oidcErrServerError
	}

	return url.Values{"token_hash": {link.HashedToken}, "type": {"magiclink"}}.Encode(), ""
}

// oidcExternalID keys a provider account in external_identities. The subject
// is unique and stable only within its issuer (OIDC Core 2), so both are kept,
// readable for operators.
func oidcExternalID(issuer, subject string) string {
	return issuer + "|" + subject
}

// oidcPlaceholderEmail is the GoTrue email of a provider account. The subject
// may hold any character, so the local part is a hash of the external ID.
func oidcPlaceholderEmail(providerID, issuer, subject string) string {
	sum := sha256.Sum256([]byte(oidcExternalID(issuer, subject)))
	return hex.EncodeToString(sum[:])[:32] + "@" + providerID + "." + oidcEmailDomain
}

func (o *OIDCLogin) newLoginState(redirectTo string) (*oidcLoginState, error) {
	state, err := randomToken()
	if err != nil {
		return nil, err
	}

	nonce, err := randomToken()
	if err != nil {
		return nil, err
	}

	return &oidcLoginState{
		State:      state,
		Nonce:      nonce,
		Verifier:   oauth2.GenerateVerifier(),
		RedirectTo: redirectTo,
		ExpiresAt:  o.now().Add(oidcStateTTL).Unix(),
	}, nil
}

// takeLoginState reads the login state cookie and clears it, so the state is
// good for one callback only.
func (o *OIDCLogin) takeLoginState(c *gin.Context) (*oidcLoginState, error) {
	cookie, err := c.Request.Cookie(oidcStateCookie)
	if err != nil {
		return nil, fmt.Errorf("%w: no login state cookie", errInvalidLoginState)
	}

	o.setStateCookie(c, "", -1)

	return o.codec.open(cookie.Value, o.now())
}

func (o *OIDCLogin) setStateCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    value,
		Path:     o.cookiePath,
		MaxAge:   maxAge,
		Secure:   o.cookieSecure,
		HttpOnly: true,
		// Lax, not Strict: the provider sends the browser back with a top-level
		// cross-site GET, which must carry the cookie.
		SameSite: http.SameSiteLaxMode,
	})
}

func (o *OIDCLogin) redirectWithError(c *gin.Context, redirectTo, code string) {
	c.Redirect(http.StatusFound, redirectTo+"#"+url.Values{"error": {code}}.Encode())
}

// allowedRedirect returns the normalized redirect target, or false when it is
// not under an allowed redirect. An empty target is the first allowed one.
func (o *OIDCLogin) allowedRedirect(raw string) (string, bool) {
	if raw == "" {
		return o.allowedRedirects[0].String(), true
	}

	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || strings.ContainsAny(raw, "#\\") {
		return "", false
	}

	// A path that cleans to something else, e.g. /ui/../admin, could escape the
	// allowed prefix once the browser resolves it.
	if u.Path == "" {
		u.Path = "/"
	}

	cleaned := path.Clean(u.Path)
	if strings.HasSuffix(u.Path, "/") && cleaned != "/" {
		cleaned += "/"
	}

	if cleaned != u.Path {
		return "", false
	}

	for _, allowed := range o.allowedRedirects {
		if !strings.EqualFold(u.Scheme, allowed.Scheme) || !strings.EqualFold(u.Host, allowed.Host) {
			continue
		}

		if strings.HasPrefix(u.Path, allowed.Path) || u.Path+"/" == allowed.Path {
			return u.String(), true
		}
	}

	return "", false
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}
