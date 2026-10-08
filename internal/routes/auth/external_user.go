package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/supabase-community/gotrue-go/types"

	"github.com/neutree-ai/neutree/internal/auth"
)

// linkRetryAttempts and linkRetryDelay bound how long a first login waits for a
// concurrent first login of the same account to finish linking it. Variables so
// tests can shorten them.
var (
	linkRetryAttempts = auth.DefaultLinkRetryAttempts
	linkRetryDelay    = auth.DefaultLinkRetryDelay
)

// errUserDisabled means the linked user is banned in GoTrue, e.g. because the
// organization sync found it disabled or gone in the directory.
var errUserDisabled = errors.New("user is disabled")

// msgUserDisabled answers a login of a disabled user. The credentials were
// right, so saying so tells nothing an attacker could use.
const msgUserDisabled = "account is disabled"

// ensureExternalUser returns the GoTrue user linked to the account, creating
// and linking one on the account's first login (unless the organization sync
// created it already). Accounts are matched by their link only, never by
// email or name.
func ensureExternalUser(ctx context.Context, deps *Dependencies, account auth.ExternalAccount) (string, error) {
	users := &auth.ExternalUsers{
		Client:        deps.AuthClient,
		Links:         deps.Storage,
		RetryAttempts: linkRetryAttempts,
		RetryDelay:    linkRetryDelay,
	}

	userID, _, err := users.Ensure(ctx, account)

	return userID, err
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

	// GoTrue would refuse the session of a banned user too; checking here
	// gives the login a clear answer instead of a failed verify.
	if user != nil && auth.IsBanned(&user.User, time.Now()) {
		return "", fmt.Errorf("user %s: %w", userID, errUserDisabled)
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
