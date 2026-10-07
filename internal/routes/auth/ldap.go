package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/identity/ldap"
)

const (
	// ldapEmailDomain holds the placeholder emails of LDAP users. GoTrue needs an
	// email to issue a session, and the directory email cannot be used: it may be
	// missing, change, or already belong to a local user.
	ldapEmailDomain = "ldap.neutree.local"

	msgInvalidCredentials = "invalid username or password"
	msgDirectoryDown      = "directory service unavailable"
	msgInternalError      = "internal error"
)

// LDAPAuthenticator checks credentials against the directory; *ldap.Authenticator
// implements it.
type LDAPAuthenticator interface {
	Authenticate(ctx context.Context, username, password string) (*ldap.Identity, error)
}

type LDAPTokenRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLDAPToken logs a user in with directory credentials and answers with a
// GoTrue session, the same body as a password grant on /token.
//
// The directory account is linked to a GoTrue user by its stable directory ID.
// The first login creates that user; later logins reuse the link. GoTrue then
// issues the session through an admin magic link, which needs no password.
func handleLDAPToken(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req LDAPTokenRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		ctx := c.Request.Context()

		identity, err := deps.LDAP.Authenticate(ctx, req.Username, req.Password)
		if err != nil {
			status, msg := ldapErrorResponse(req.Username, err)
			c.JSON(status, gin.H{"error": msg})

			return
		}

		userID, err := ensureLDAPUser(ctx, deps, identity)
		if err != nil {
			klog.Errorf("LDAP login of %q (id %s): link user: %v", identity.Username, identity.ExternalID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalError})

			return
		}

		session, err := issueSession(ctx, deps, userID)
		if err != nil {
			klog.Errorf("LDAP login of %q (user %s): issue session: %v", identity.Username, userID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalError})

			return
		}

		c.Data(http.StatusOK, "application/json", session)
	}
}

// ldapErrorResponse maps an Authenticate error to a status and message, and
// logs it. Every credential failure gets the same message so the response does
// not tell which usernames exist.
func ldapErrorResponse(username string, err error) (int, string) {
	switch {
	case errors.Is(err, ldap.ErrMultipleUsers):
		klog.Warningf("LDAP login of %q rejected: the user filter matches several entries: %v", username, err)
		return http.StatusUnauthorized, msgInvalidCredentials
	case errors.Is(err, ldap.ErrInvalidCredentials), errors.Is(err, ldap.ErrUserNotFound):
		klog.Infof("LDAP login of %q rejected: %v", username, err)
		return http.StatusUnauthorized, msgInvalidCredentials
	case errors.Is(err, ldap.ErrConnection), errors.Is(err, ldap.ErrTLS), errors.Is(err, ldap.ErrServiceBindFailed):
		klog.Errorf("LDAP login of %q: directory unavailable: %v", username, err)
		return http.StatusServiceUnavailable, msgDirectoryDown
	default:
		klog.Errorf("LDAP login of %q failed: %v", username, err)
		return http.StatusInternalServerError, msgInternalError
	}
}

func ldapPlaceholderEmail(externalID string) string {
	return strings.ToLower(externalID) + "@" + ldapEmailDomain
}

// ensureLDAPUser returns the GoTrue user linked to the directory account,
// creating and linking one on the account's first login.
func ensureLDAPUser(ctx context.Context, deps *Dependencies, identity *ldap.Identity) (string, error) {
	return ensureExternalUser(ctx, deps, externalAccount{
		Source:         auth.LDAPSource,
		ExternalID:     identity.ExternalID,
		IdentitySource: auth.LDAPSource,
		Email:          ldapPlaceholderEmail(identity.ExternalID),
		Metadata:       externalUserMetadata(identity.Username, identity.DisplayName, identity.Email),
		LogName:        identity.Username,
	})
}
