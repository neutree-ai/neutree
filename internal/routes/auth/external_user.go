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
	linkRetryAttempts = auth.DefaultLinkRetryAttempts
	linkRetryDelay    = auth.DefaultLinkRetryDelay
)

// errUserDisabled means the linked user is banned in GoTrue, e.g. because the
// organization sync found it disabled or gone in the directory.
var errUserDisabled = errors.New("user is disabled")

// msgUserDisabled answers a login of a disabled user. The credentials were
// right, so saying so tells nothing an attacker could use.
const msgUserDisabled = "account is disabled"

// ensureExternalUser returns the link of the account to its GoTrue user,
// creating and linking a user on the account's first login (unless the
// organization sync created it already); created tells which. Accounts are
// matched by their link only, never by email or name.
func ensureExternalUser(ctx context.Context, deps *Dependencies, account auth.ExternalAccount) (link *storage.ExternalIdentity, created bool, err error) {
	users := &auth.ExternalUsers{
		Client:        deps.AuthClient,
		Links:         deps.Storage,
		RetryAttempts: linkRetryAttempts,
		RetryDelay:    linkRetryDelay,
	}

	return users.Ensure(ctx, account)
}

// loginUser returns the GoTrue user userID for a login. A banned user is
// refused with errUserDisabled: GoTrue would refuse its session too, and
// checking here gives the login a clear answer instead of a failed verify.
func loginUser(deps *Dependencies, userID string) (*types.User, error) {
	id, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID %q: %w", userID, err)
	}

	resp, err := deps.AuthClient.AdminGetUser(types.AdminGetUserRequest{UserID: id})
	if err != nil {
		return nil, fmt.Errorf("get user %s: %w", userID, err)
	}

	if resp == nil || resp.Email == "" {
		return nil, fmt.Errorf("user %s has no email", userID)
	}

	if auth.IsBanned(&resp.User, time.Now()) {
		return nil, fmt.Errorf("user %s: %w", userID, errUserDisabled)
	}

	return &resp.User, nil
}

// refreshExternalUser brings the user's identity source name, display name and
// email in line with what the identity source said at this login (D56). The
// login goes on when this fails: the stale values are only shown, never used
// to authorize.
func refreshExternalUser(deps *Dependencies, account auth.ExternalAccount, link *storage.ExternalIdentity, user *types.User) {
	if err := auth.RecordIdentitySourceName(deps.AuthClient, user, account.IdentitySourceName); err != nil {
		klog.Warningf("%s login of %q (user %s): record identity source name: %v", account.Source, account.LogName, user.ID, err)
	}

	if err := auth.UpdateExternalUser(deps.AuthClient, deps.Storage, auth.ExternalUserUpdate{
		Link: link, User: user, Profile: account.Profile,
	}); err != nil {
		klog.Warningf("%s login of %q (user %s): refresh profile: %v", account.Source, account.LogName, user.ID, err)
	}
}

// magicLinkFor returns a magic link token for user. The link is asked for
// with the user's current GoTrue email, not the placeholder it was created
// with, so a login still works after the email was changed in GoTrue.
func magicLinkFor(ctx context.Context, deps *Dependencies, user *types.User) (*auth.MagicLink, error) {
	link, err := deps.Sessions.GenerateMagicLink(ctx, user.Email)
	if err != nil {
		return nil, fmt.Errorf("generate magic link: %w", err)
	}

	// GoTrue resolves the link by email, and signs up a new user when nobody has
	// it. Only a token for the linked user may be handed out.
	if !sameUserID(link.UserID, user.ID.String()) {
		return nil, fmt.Errorf("magic link is for user %s, want %s", link.UserID, user.ID)
	}

	return link, nil
}

// issueSession returns a GoTrue session for user.
func issueSession(ctx context.Context, deps *Dependencies, user *types.User) ([]byte, error) {
	link, err := magicLinkFor(ctx, deps, user)
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
