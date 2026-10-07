package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/identity/ldap"
)

const (
	// ldapEmailDomain is the parent domain of LDAP users' placeholder emails;
	// each identity source gets the subdomain <source name>.ldap.neutree.local.
	// GoTrue needs an email to issue a session, and the directory email cannot
	// be used: it may be missing, change, or already belong to a local user.
	ldapEmailDomain = "ldap.neutree.local"

	msgInvalidCredentials = "invalid username or password"
	msgDirectoryDown      = "directory service unavailable"
	msgInternalError      = "internal error"
	msgSourceNotFound     = "identity source not found"
	msgSourceUnavailable  = "identity sources are unavailable"
)

// LDAPAuthenticator checks credentials against the directory; *ldap.Authenticator
// implements it.
type LDAPAuthenticator interface {
	Authenticate(ctx context.Context, username, password string) (*ldap.Identity, error)
}

type LDAPTokenRequest struct {
	// Source names the LDAP identity source. It may be left out while exactly
	// one LDAP source is enabled.
	Source   string `json:"source"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLDAPToken logs a user in with directory credentials and answers with a
// GoTrue session, the same body as a password grant on /token.
//
// The directory account is linked to a GoTrue user by the source name and its
// stable directory ID. The first login creates that user; later logins reuse
// the link. GoTrue then issues the session through an admin magic link, which
// needs no password.
func handleLDAPToken(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req LDAPTokenRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		source, err := deps.Sources.resolve(req.Source, v1.IdentitySourceTypeLDAP)
		if err != nil {
			status, msg := sourceErrorResponse("LDAP", req.Source, err)
			c.JSON(status, gin.H{"error": msg})

			return
		}

		ctx := c.Request.Context()

		identity, err := source.ldap.Authenticate(ctx, req.Username, req.Password)
		if err != nil {
			status, msg := ldapErrorResponse(source.name, req.Username, err)
			c.JSON(status, gin.H{"error": msg})

			return
		}

		userID, err := ensureLDAPUser(ctx, deps, source.name, identity)
		if err != nil {
			klog.Errorf("LDAP login of %q via %s (id %s): link user: %v", identity.Username, source.name, identity.ExternalID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalError})

			return
		}

		session, err := issueSession(ctx, deps, userID)
		if err != nil {
			klog.Errorf("LDAP login of %q via %s (user %s): issue session: %v", identity.Username, source.name, userID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalError})

			return
		}

		c.Data(http.StatusOK, "application/json", session)
	}
}

// sourceErrorResponse maps a failure to resolve the identity source of a
// login to a status and message, and logs it. A missing, disabled or deleted
// source is one answer, 404: the enabled sources are public on the login
// page, so this tells nothing more.
func sourceErrorResponse(kind, name string, err error) (int, string) {
	switch {
	case errors.Is(err, errSourceRequired):
		return http.StatusBadRequest, "source is required: more than one " + kind + " identity source is enabled"
	case errors.Is(err, errSourceNotFound):
		klog.Infof("%s login via %q rejected: no such enabled identity source", kind, name)
		return http.StatusNotFound, msgSourceNotFound
	default:
		// errSourceUnusable, or the database could not be read.
		klog.Errorf("%s login via %q: %v", kind, name, err)
		return http.StatusServiceUnavailable, msgSourceUnavailable
	}
}

// ldapErrorResponse maps an Authenticate error to a status and message, and
// logs it. Every credential failure gets the same message so the response does
// not tell which usernames exist.
func ldapErrorResponse(source, username string, err error) (int, string) {
	switch {
	case errors.Is(err, ldap.ErrMultipleUsers):
		klog.Warningf("LDAP login of %q via %s rejected: the user filter matches several entries: %v", username, source, err)
		return http.StatusUnauthorized, msgInvalidCredentials
	case errors.Is(err, ldap.ErrInvalidCredentials), errors.Is(err, ldap.ErrUserNotFound):
		klog.Infof("LDAP login of %q via %s rejected: %v", username, source, err)
		return http.StatusUnauthorized, msgInvalidCredentials
	case errors.Is(err, ldap.ErrConnection), errors.Is(err, ldap.ErrTLS), errors.Is(err, ldap.ErrServiceBindFailed):
		klog.Errorf("LDAP login of %q via %s: directory unavailable: %v", username, source, err)
		return http.StatusServiceUnavailable, msgDirectoryDown
	default:
		klog.Errorf("LDAP login of %q via %s failed: %v", username, source, err)
		return http.StatusInternalServerError, msgInternalError
	}
}

func ldapPlaceholderEmail(sourceName, externalID string) string {
	return strings.ToLower(externalID) + "@" + sourceName + "." + ldapEmailDomain
}

// ensureLDAPUser returns the GoTrue user linked to the directory account,
// creating and linking one on the account's first login.
func ensureLDAPUser(ctx context.Context, deps *Dependencies, sourceName string, identity *ldap.Identity) (string, error) {
	return ensureExternalUser(ctx, deps, externalAccount{
		Source:         auth.LinkSource(auth.LDAPSource, sourceName),
		ExternalID:     identity.ExternalID,
		IdentitySource: auth.LDAPSource,
		Email:          ldapPlaceholderEmail(sourceName, identity.ExternalID),
		Metadata:       externalUserMetadata(identity.Username, identity.DisplayName, identity.Email),
		LogName:        identity.Username,
	})
}
