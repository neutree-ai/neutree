package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/supabase-community/gotrue-go/types"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// linkRetryAttempts and linkRetryDelay bound how long a first login waits for a
// concurrent first login of the same account to finish linking it. Variables so
// tests can shorten them.
var (
	linkRetryAttempts = 5
	linkRetryDelay    = 200 * time.Millisecond
)

// externalAccount is an account authenticated by an external identity source,
// as needed to find or create the GoTrue user it owns.
type externalAccount struct {
	// Source and ExternalID key the account in external_identities.
	Source     string
	ExternalID string
	// IdentitySource is recorded in the user's app_metadata; it selects the
	// fields neutree must not change for the user.
	IdentitySource string
	// Email is the placeholder GoTrue email of a user created for the account,
	// derived from ExternalID. Later logins use whatever email the user has
	// then. The source's own email is never used: it may be missing, change,
	// or already belong to a local user.
	Email string
	// Metadata becomes the new user's user_metadata.
	Metadata map[string]any
	// LogName names the account in logs.
	LogName string
}

// externalUserMetadata builds user_metadata for an externally managed user.
// There is no username key: api.handle_new_user then derives the profile name
// from preferred_username, the display name from name, and the profile email
// from email.
func externalUserMetadata(username, displayName, email string) map[string]any {
	metadata := map[string]any{}

	if username != "" {
		metadata["preferred_username"] = username
	}

	if displayName != "" {
		metadata["name"] = displayName
	}

	if email != "" {
		metadata["email"] = email
	}

	return metadata
}

// ensureExternalUser returns the GoTrue user linked to the account, creating
// and linking one on the account's first login. Accounts are matched by their
// link only, never by email or name.
func ensureExternalUser(ctx context.Context, deps *Dependencies, account externalAccount) (string, error) {
	link, err := deps.Storage.GetExternalIdentity(account.Source, account.ExternalID)
	if err == nil {
		return link.UserID, nil
	}

	if !errors.Is(err, storage.ErrResourceNotFound) {
		return "", fmt.Errorf("look up link: %w", err)
	}

	return createExternalUser(ctx, deps, account)
}

func createExternalUser(ctx context.Context, deps *Dependencies, account externalAccount) (string, error) {
	created, err := deps.AuthClient.AdminCreateUser(types.AdminCreateUserRequest{
		Email:        account.Email,
		EmailConfirm: true,
		UserMetadata: account.Metadata,
		AppMetadata:  map[string]any{auth.IdentitySourceKey: account.IdentitySource},
	})
	if err != nil {
		// A concurrent first login of the same account created the user first, so
		// the placeholder email is taken; its link follows shortly.
		if link, lerr := waitForLink(ctx, deps.Storage, account.Source, account.ExternalID); lerr == nil {
			return link.UserID, nil
		}

		return "", fmt.Errorf("create GoTrue user: %w", err)
	}

	userID := created.ID.String()

	err = deps.Storage.CreateExternalIdentity(&storage.ExternalIdentity{
		Source:     account.Source,
		ExternalID: account.ExternalID,
		UserID:     userID,
	})
	if err == nil {
		klog.Infof("%s login of %q (id %s): created user %s", account.Source, account.LogName, account.ExternalID, userID)
		return userID, nil
	}

	// The user is unusable without its link, and would block the next attempt
	// by holding the placeholder email.
	if derr := deps.AuthClient.AdminDeleteUser(types.AdminDeleteUserRequest{UserID: created.ID}); derr != nil {
		klog.Errorf("%s login of %q: failed to delete unlinked user %s: %v", account.Source, account.LogName, userID, derr)
	}

	if !errors.Is(err, storage.ErrResourceConflict) {
		return "", fmt.Errorf("link user %s: %w", userID, err)
	}

	// Another login linked the account first; use its user.
	link, err := deps.Storage.GetExternalIdentity(account.Source, account.ExternalID)
	if err != nil {
		return "", fmt.Errorf("look up link after losing the race: %w", err)
	}

	return link.UserID, nil
}

func waitForLink(ctx context.Context, store storage.Storage, source, externalID string) (*storage.ExternalIdentity, error) {
	var err error

	for attempt := 0; attempt < linkRetryAttempts; attempt++ {
		var link *storage.ExternalIdentity

		link, err = store.GetExternalIdentity(source, externalID)
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

// magicLinkFor returns a magic link token for userID. The link is asked for
// with the user's current GoTrue email, not the placeholder it was created
// with, so a login still works after the email was changed in GoTrue.
func magicLinkFor(ctx context.Context, deps *Dependencies, userID string) (*auth.MagicLink, error) {
	email, err := currentEmail(deps, userID)
	if err != nil {
		return nil, err
	}

	link, err := deps.Sessions.GenerateMagicLink(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("generate magic link: %w", err)
	}

	// GoTrue resolves the link by email, and signs up a new user when nobody has
	// it. Only a token for the linked user may be handed out.
	if !sameUserID(link.UserID, userID) {
		return nil, fmt.Errorf("magic link is for user %s, want %s", link.UserID, userID)
	}

	return link, nil
}

func currentEmail(deps *Dependencies, userID string) (string, error) {
	id, err := uuid.Parse(userID)
	if err != nil {
		return "", fmt.Errorf("invalid user ID %q: %w", userID, err)
	}

	user, err := deps.AuthClient.AdminGetUser(types.AdminGetUserRequest{UserID: id})
	if err != nil {
		return "", fmt.Errorf("get user %s: %w", userID, err)
	}

	if user == nil || user.Email == "" {
		return "", fmt.Errorf("user %s has no email", userID)
	}

	return user.Email, nil
}

// issueSession returns a GoTrue session for userID.
func issueSession(ctx context.Context, deps *Dependencies, userID string) ([]byte, error) {
	link, err := magicLinkFor(ctx, deps, userID)
	if err != nil {
		return nil, err
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
