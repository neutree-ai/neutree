package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// placeholderEmail is the GoTrue email of an external account. The external
// ID is case-sensitive and may hold any character, so the local part is its
// hash.
func placeholderEmail(sourceName, domain, externalID string) string {
	sum := sha256.Sum256([]byte(externalID))
	return hex.EncodeToString(sum[:])[:32] + "@" + sourceName + "." + domain
}

// externalAccount is an account authenticated by an external identity source,
// as needed to find or create the GoTrue user it owns.
type externalAccount struct {
	// Source and ExternalID key the account in external_identities.
	Source     string
	ExternalID string
	// IdentitySource is recorded in the user's app_metadata; it selects the
	// fields neutree must not change for the user.
	IdentitySource string
	// IdentitySourceName is the name of the IdentitySource, recorded in the
	// user's app_metadata for display.
	IdentitySourceName string
	// Email is the placeholder GoTrue email of a user created for the account,
	// derived from ExternalID. Later logins use whatever email the user has
	// then. The source's own email is never used: it may be missing, change,
	// or already belong to a local user.
	Email string
	// Profile is what the source says about the account: it becomes the new
	// user's user_metadata, and refreshes it on later logins.
	Profile auth.ExternalProfile
	// LogName names the account in logs.
	LogName string
}

// errUserDisabled means the linked user is banned in GoTrue.
var errUserDisabled = errors.New("user is disabled")

// msgUserDisabled answers a login of a disabled user. The credentials were
// right, so saying so tells nothing an attacker could use.
const msgUserDisabled = "account is disabled"

// ensureExternalUser returns the GoTrue user linked to the account, creating
// and linking one on the account's first login; created tells which.
// Accounts are matched by their link only, never by email or name.
func ensureExternalUser(ctx context.Context, deps *Dependencies, account externalAccount) (userID string, created bool, err error) {
	link, err := deps.Storage.GetExternalIdentity(account.Source, account.ExternalID)
	if err == nil {
		return link.UserID, false, nil
	}

	if !errors.Is(err, storage.ErrResourceNotFound) {
		return "", false, fmt.Errorf("look up link: %w", err)
	}

	return createExternalUser(ctx, deps, account)
}

func createExternalUser(ctx context.Context, deps *Dependencies, account externalAccount) (string, bool, error) {
	created, err := deps.AuthClient.AdminCreateUser(types.AdminCreateUserRequest{
		Email:        account.Email,
		EmailConfirm: true,
		UserMetadata: auth.ExternalUserMetadata(account.Profile.Username, account.Profile.DisplayName, account.Profile.Email),
		AppMetadata:  auth.ExternalAppMetadata(account.IdentitySource, account.IdentitySourceName),
	})
	if err != nil {
		// A concurrent first login of the same account created the user first, so
		// the placeholder email is taken; its link follows shortly.
		if link, lerr := waitForLink(ctx, deps.Storage, account.Source, account.ExternalID); lerr == nil {
			return link.UserID, false, nil
		}

		return "", false, fmt.Errorf("create GoTrue user: %w", err)
	}

	userID := created.ID.String()

	err = deps.Storage.CreateExternalIdentity(&storage.ExternalIdentity{
		Source:     account.Source,
		ExternalID: account.ExternalID,
		UserID:     userID,
	})
	if err == nil {
		klog.Infof("%s login of %q (id %s): created user %s", account.Source, account.LogName, account.ExternalID, userID)
		return userID, true, nil
	}

	// The user is unusable without its link, and would block the next attempt
	// by holding the placeholder email.
	if derr := deps.AuthClient.AdminDeleteUser(types.AdminDeleteUserRequest{UserID: created.ID}); derr != nil {
		klog.Errorf("%s login of %q: failed to delete unlinked user %s: %v", account.Source, account.LogName, userID, derr)
	}

	if !errors.Is(err, storage.ErrResourceConflict) {
		return "", false, fmt.Errorf("link user %s: %w", userID, err)
	}

	// Another login linked the account first; use its user.
	link, err := deps.Storage.GetExternalIdentity(account.Source, account.ExternalID)
	if err != nil {
		return "", false, fmt.Errorf("look up link after losing the race: %w", err)
	}

	return link.UserID, false, nil
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
func refreshExternalUser(deps *Dependencies, account externalAccount, user *types.User, profile auth.ExternalProfile) {
	if err := auth.RecordIdentitySourceName(deps.AuthClient, user, account.IdentitySourceName); err != nil {
		klog.Warningf("%s login of %q (user %s): record identity source name: %v", account.Source, account.LogName, user.ID, err)
	}

	if err := auth.UpdateExternalUser(deps.AuthClient, deps.Storage, user.ID.String(), user, profile, false); err != nil {
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
