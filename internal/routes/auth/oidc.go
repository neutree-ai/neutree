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

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/identity/oidc"
)

// oidcEmailDomain is the parent domain of OIDC users' placeholder emails; each
// identity source gets the subdomain <source name>.oidc.neutree.local.
const oidcEmailDomain = "oidc.neutree.local"

// Error codes the callback puts in the redirect fragment as error=<code>. They
// are deliberately coarse; the details are logged.
const (
	oidcErrInvalidState   = "invalid_state"
	oidcErrIdPError       = "idp_error"
	oidcErrIdPUnavailable = "idp_unavailable"
	oidcErrLoginFailed    = "login_failed"
	oidcErrServerError    = "server_error"
	// oidcErrSourceUnavailable means the identity source the login started
	// with was disabled or deleted before it finished.
	oidcErrSourceUnavailable = "source_unavailable"
)

// OIDCRelyingParty runs the code flow with the provider; *oidc.RelyingParty
// implements it.
type OIDCRelyingParty interface {
	AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error)
	Exchange(ctx context.Context, code, verifier, nonce string) (*oidc.Identity, error)
}

// parseOIDCAllowedRedirects parses the UI locations an OIDC login may return
// to. Each must be an absolute http(s) URL without query or fragment; its path
// is a prefix, so it gets a trailing '/'.
func parseOIDCAllowedRedirects(raws []string) ([]*url.URL, error) {
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

// handleOIDCAuthorize starts a login with the OIDC identity source named by
// the source query parameter (optional while exactly one is enabled): it
// remembers the login in the state cookie and sends the browser to the
// provider. Until the source and redirect_to are known to be good there is no
// safe place to redirect to, so those failures answer with JSON.
func handleOIDCAuthorize(deps *Dependencies) gin.HandlerFunc {
	sources := deps.Sources

	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")

		source, err := sources.resolve(c.Query("source"), v1.IdentitySourceTypeOIDC)
		if err != nil {
			status, msg := sourceErrorResponse("OIDC", c.Query("source"), err)
			c.JSON(status, gin.H{"error": msg})

			return
		}

		o := source.oidc

		redirectTo, ok := o.allowedRedirect(c.Query("redirect_to"))
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "redirect_to is not an allowed redirect target"})
			return
		}

		state, err := newLoginState(redirectTo, sources.now())
		if err != nil {
			klog.Errorf("OIDC login via %s: new login state: %v", o.name, err)
			redirectWithError(c, redirectTo, oidcErrServerError)

			return
		}

		authURL, err := o.rp.AuthCodeURL(c.Request.Context(), state.State, state.Nonce, state.Verifier)
		if err != nil {
			klog.Errorf("OIDC login via %s: %v", o.name, err)
			redirectWithError(c, redirectTo, oidcErrIdPUnavailable)

			return
		}

		sealed, err := sources.codec.seal(o.name, state)
		if err != nil {
			klog.Errorf("OIDC login via %s: seal login state: %v", o.name, err)
			redirectWithError(c, redirectTo, oidcErrServerError)

			return
		}

		setStateCookie(c, o.cookiePath, o.cookieSecure, sealed, int(oidcStateTTL/time.Second))
		c.Redirect(http.StatusFound, authURL)
	}
}

// handleOIDCCallback finishes a login. One callback URL serves every OIDC
// source: the source is the one the state cookie was sealed for, never a
// request parameter. The provider's account is linked to a GoTrue user like an
// LDAP account, and the browser goes back to the UI with a one-time magic link
// token in the URL fragment, which the UI exchanges on /auth/verify for a
// session. The fragment never reaches a server, so the token stays out of
// access logs and Referer headers.
func handleOIDCCallback(deps *Dependencies) gin.HandlerFunc {
	sources := deps.Sources

	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")

		// The state is good for one callback only. The cookie is cleared on the
		// path it was set for, which is the path of this request.
		cookie, err := c.Request.Cookie(oidcStateCookie)
		if err == nil {
			setStateCookie(c, c.Request.URL.Path, c.Request.TLS != nil, "", -1)
		}

		if err != nil {
			klog.Infof("OIDC callback rejected: no login state cookie")
			redirectWithError(c, sources.fallbackRedirect(""), oidcErrInvalidState)

			return
		}

		// Nothing in an unopened state can be trusted, not even where to send the
		// browser back, so the browser goes to a fallback from an allow list.
		name, state, err := sources.codec.open(cookie.Value, sources.now())
		if err != nil {
			klog.Infof("OIDC callback rejected: %v", err)
			redirectWithError(c, sources.fallbackRedirect(cookie.Value), oidcErrInvalidState)

			return
		}

		if subtle.ConstantTimeCompare([]byte(c.Query("state")), []byte(state.State)) != 1 {
			klog.Infof("OIDC callback of %s rejected: state does not match the login state cookie", name)
			redirectWithError(c, state.RedirectTo, oidcErrInvalidState)

			return
		}

		source, err := sources.get(name, v1.IdentitySourceTypeOIDC)
		if err != nil {
			if errors.Is(err, errSourceNotFound) {
				klog.Infof("OIDC callback of %s rejected: the identity source is gone or disabled", name)
				redirectWithError(c, state.RedirectTo, oidcErrSourceUnavailable)

				return
			}

			klog.Errorf("OIDC callback of %s: %v", name, err)
			redirectWithError(c, state.RedirectTo, oidcErrIdPUnavailable)

			return
		}

		o := source.oidc

		if idpErr := c.Query("error"); idpErr != "" {
			klog.Infof("OIDC login via %s refused by the provider: %s: %s", o.name, idpErr, c.Query("error_description"))
			redirectWithError(c, state.RedirectTo, oidcErrIdPError)

			return
		}

		fragment, code := o.finishLogin(c.Request.Context(), deps, c.Query("code"), state)
		if code != "" {
			redirectWithError(c, state.RedirectTo, code)
			return
		}

		c.Redirect(http.StatusFound, state.RedirectTo+"#"+fragment)
	}
}

// finishLogin redeems the code and returns the redirect fragment carrying the
// magic link token, or an error code.
func (o *oidcSource) finishLogin(ctx context.Context, deps *Dependencies, code string, state *oidcLoginState) (string, string) {
	if code == "" {
		klog.Infof("OIDC callback of %s rejected: no code", o.name)
		return "", oidcErrLoginFailed
	}

	identity, err := o.rp.Exchange(ctx, code, state.Verifier, state.Nonce)
	if err != nil {
		klog.Warningf("OIDC login via %s failed: %v", o.name, err)

		if errors.Is(err, oidc.ErrDiscovery) {
			return "", oidcErrIdPUnavailable
		}

		return "", oidcErrLoginFailed
	}

	if identity.Issuer == "" || identity.Subject == "" {
		klog.Warningf("OIDC login via %s failed: ID token has no issuer or subject", o.name)
		return "", oidcErrLoginFailed
	}

	logName := identity.Username
	if logName == "" {
		logName = identity.Subject
	}

	userID, err := ensureExternalUser(ctx, deps, auth.ExternalAccount{
		Source:         auth.LinkSource(auth.OIDCSource, o.name),
		ExternalID:     oidcExternalID(identity.Issuer, identity.Subject),
		IdentitySource: auth.OIDCSource,
		Email:          oidcPlaceholderEmail(o.name, identity.Issuer, identity.Subject),
		Username:       identity.Username,
		DisplayName:    identity.DisplayName,
		SourceEmail:    identity.Email,
		LogName:        logName,
	})
	if err != nil {
		klog.Errorf("OIDC login of %q via %s: link user: %v", logName, o.name, err)
		return "", oidcErrServerError
	}

	link, err := magicLinkFor(ctx, deps, userID)
	if err != nil {
		klog.Errorf("OIDC login of %q via %s (user %s): %v", logName, o.name, userID, err)
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
func oidcPlaceholderEmail(sourceName, issuer, subject string) string {
	sum := sha256.Sum256([]byte(oidcExternalID(issuer, subject)))
	return hex.EncodeToString(sum[:])[:32] + "@" + sourceName + "." + oidcEmailDomain
}

func newLoginState(redirectTo string, now time.Time) (*oidcLoginState, error) {
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
		ExpiresAt:  now.Add(oidcStateTTL).Unix(),
	}, nil
}

func setStateCookie(c *gin.Context, path string, secure bool, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    value,
		Path:     path,
		MaxAge:   maxAge,
		Secure:   secure,
		HttpOnly: true,
		// Lax, not Strict: the provider sends the browser back with a top-level
		// cross-site GET, which must carry the cookie.
		SameSite: http.SameSiteLaxMode,
	})
}

// fallbackRedirect is where a callback whose login state cannot be read sends
// the browser. The redirect_to of the login is unknown then, so it is never
// taken from the request; the target is always an allowed redirect of an
// enabled OIDC source, or the same-origin root:
//
//   - the first allowed redirect of the source the unreadable cookie names
//     before its '.', when that is an enabled OIDC source. The name is not
//     authenticated, but it only selects among allowed redirects;
//   - otherwise the first allowed redirect of the only enabled OIDC source;
//   - otherwise "/", which neutree-api serves the UI on.
func (s *LoginSources) fallbackRedirect(sealed string) string {
	if name, _, found := strings.Cut(sealed, "."); found && name != "" {
		if source, err := s.get(name, v1.IdentitySourceTypeOIDC); err == nil {
			return source.oidc.allowedRedirects[0].String()
		}
	}

	if source, err := s.resolve("", v1.IdentitySourceTypeOIDC); err == nil {
		return source.oidc.allowedRedirects[0].String()
	}

	return "/"
}

func redirectWithError(c *gin.Context, redirectTo, code string) {
	c.Redirect(http.StatusFound, redirectTo+"#"+url.Values{"error": {code}}.Encode())
}

// allowedRedirect returns the normalized redirect target, or false when it is
// not under an allowed redirect. An empty target is the first allowed one.
func (o *oidcSource) allowedRedirect(raw string) (string, bool) {
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
