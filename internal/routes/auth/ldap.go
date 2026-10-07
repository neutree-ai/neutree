package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/supabase-community/gotrue-go/types"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/identity/ldap"
	"github.com/neutree-ai/neutree/pkg/storage"
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

// linkRetryAttempts and linkRetryDelay bound how long a first login waits for a
// concurrent first login of the same account to finish linking it. Variables so
// tests can shorten them.
var (
	linkRetryAttempts = 5
	linkRetryDelay    = 200 * time.Millisecond
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

		session, err := issueSession(ctx, deps, userID, ldapPlaceholderEmail(identity.ExternalID))
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
	link, err := deps.Storage.GetExternalIdentity(auth.LDAPSource, identity.ExternalID)
	if err == nil {
		return link.UserID, nil
	}

	if !errors.Is(err, storage.ErrResourceNotFound) {
		return "", fmt.Errorf("look up link: %w", err)
	}

	return createLDAPUser(ctx, deps, identity)
}

func createLDAPUser(ctx context.Context, deps *Dependencies, identity *ldap.Identity) (string, error) {
	// No username key: api.handle_new_user then derives the profile name from
	// preferred_username, and the display name from name.
	metadata := map[string]any{"preferred_username": identity.Username}
	if identity.DisplayName != "" {
		metadata["name"] = identity.DisplayName
	}

	if identity.Email != "" {
		metadata["email"] = identity.Email
	}

	created, err := deps.AuthClient.AdminCreateUser(types.AdminCreateUserRequest{
		Email:        ldapPlaceholderEmail(identity.ExternalID),
		EmailConfirm: true,
		UserMetadata: metadata,
		AppMetadata:  map[string]any{auth.IdentitySourceKey: auth.LDAPSource},
	})
	if err != nil {
		// A concurrent first login of the same account created the user first, so
		// the placeholder email is taken; its link follows shortly.
		if link, lerr := waitForLink(ctx, deps.Storage, identity.ExternalID); lerr == nil {
			return link.UserID, nil
		}

		return "", fmt.Errorf("create GoTrue user: %w", err)
	}

	userID := created.ID.String()

	err = deps.Storage.CreateExternalIdentity(&storage.ExternalIdentity{
		Source:     auth.LDAPSource,
		ExternalID: identity.ExternalID,
		UserID:     userID,
	})
	if err == nil {
		klog.Infof("LDAP login of %q (id %s): created user %s", identity.Username, identity.ExternalID, userID)
		return userID, nil
	}

	// The user is unusable without its link, and would block the next attempt
	// by holding the placeholder email.
	if derr := deps.AuthClient.AdminDeleteUser(types.AdminDeleteUserRequest{UserID: created.ID}); derr != nil {
		klog.Errorf("LDAP login of %q: failed to delete unlinked user %s: %v", identity.Username, userID, derr)
	}

	if !errors.Is(err, storage.ErrResourceConflict) {
		return "", fmt.Errorf("link user %s: %w", userID, err)
	}

	// Another login linked the account first; use its user.
	link, err := deps.Storage.GetExternalIdentity(auth.LDAPSource, identity.ExternalID)
	if err != nil {
		return "", fmt.Errorf("look up link after losing the race: %w", err)
	}

	return link.UserID, nil
}

func waitForLink(ctx context.Context, store storage.Storage, externalID string) (*storage.ExternalIdentity, error) {
	var err error

	for attempt := 0; attempt < linkRetryAttempts; attempt++ {
		var link *storage.ExternalIdentity

		link, err = store.GetExternalIdentity(auth.LDAPSource, externalID)
		if err == nil {
			return link, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(linkRetryDelay):
		}
	}

	return nil, err
}

// issueSession returns a GoTrue session for userID, whose email is email.
func issueSession(ctx context.Context, deps *Dependencies, userID, email string) ([]byte, error) {
	link, err := deps.Sessions.GenerateMagicLink(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("generate magic link: %w", err)
	}

	// GoTrue resolves the link by email, and signs up a new user when nobody has
	// it. Only a session for the linked user may be handed out.
	if !sameUserID(link.UserID, userID) {
		return nil, fmt.Errorf("magic link is for user %s, want %s", link.UserID, userID)
	}

	session, err := deps.Sessions.VerifyMagicLink(ctx, link.HashedToken)
	if err != nil {
		return nil, fmt.Errorf("verify magic link: %w", err)
	}

	return session, nil
}

func sameUserID(a, b string) bool {
	ua, err := uuid.Parse(a)
	if err != nil {
		return false
	}

	ub, err := uuid.Parse(b)
	if err != nil {
		return false
	}

	return ua == ub
}
