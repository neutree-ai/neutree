package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/supabase-community/gotrue-go/types"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// LDAPEmailDomain is the parent domain of LDAP users' placeholder emails; each
// identity source gets the subdomain <source name>.ldap.neutree.local. GoTrue
// needs an email to issue a session, and the directory email cannot be used:
// it may be missing, change, or already belong to a local user.
const LDAPEmailDomain = "ldap.neutree.local"

// LDAPPlaceholderEmail is the GoTrue email of a directory account.
func LDAPPlaceholderEmail(sourceName, externalID string) string {
	return strings.ToLower(externalID) + "@" + sourceName + "." + LDAPEmailDomain
}

// ExternalAccount is an account of an external identity source, as needed to
// find or create the GoTrue user it owns.
type ExternalAccount struct {
	// Source and ExternalID key the account in external_identities.
	Source     string
	ExternalID string
	// IdentitySource is recorded in the user's app_metadata; it selects the
	// fields neutree must not change for the user.
	IdentitySource string
	// Email is the placeholder GoTrue email of a user created for the account,
	// derived from ExternalID. The source's own email is never used for it: it
	// may be missing, change, or already belong to a local user.
	Email string
	// Username, DisplayName and SourceEmail are what the source says about
	// the account; they become the new user's user_metadata and are recorded
	// on the link.
	Username    string
	DisplayName string
	SourceEmail string
	// LogName names the account in logs.
	LogName string
}

// LDAPAccount describes a directory account of the LDAP identity source
// sourceName. A login and the organization sync build it the same way, so a
// user created by either is the same user to the other.
func LDAPAccount(sourceName, externalID, username, displayName, email string) ExternalAccount {
	return ExternalAccount{
		Source:         LinkSource(LDAPSource, sourceName),
		ExternalID:     externalID,
		IdentitySource: LDAPSource,
		Email:          LDAPPlaceholderEmail(sourceName, externalID),
		Username:       username,
		DisplayName:    displayName,
		SourceEmail:    email,
		LogName:        username,
	}
}

// ExternalUserMetadata builds user_metadata for an externally managed user.
// There is no username key: api.handle_new_user then derives the profile name
// from preferred_username, the display name from name, and the profile email
// from email.
func ExternalUserMetadata(username, displayName, email string) map[string]any {
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

// ExternalIdentityStore reads and writes account links; storage.Storage
// implements it.
type ExternalIdentityStore interface {
	GetExternalIdentity(source, externalID string) (*storage.ExternalIdentity, error)
	CreateExternalIdentity(data *storage.ExternalIdentity) error
}

// Default retry bounds of ExternalUsers: how long creating a user waits for a
// concurrent creation of the same account to finish linking it.
const (
	DefaultLinkRetryAttempts = 5
	DefaultLinkRetryDelay    = 200 * time.Millisecond
)

// ExternalUsers finds and creates the GoTrue users of external accounts. The
// login routes of neutree-api use it on a first login, the organization sync
// of neutree-core to create users before they log in.
type ExternalUsers struct {
	Client Client
	Links  ExternalIdentityStore
	// RetryAttempts and RetryDelay bound the wait for a concurrent creation;
	// a zero RetryAttempts means DefaultLinkRetryAttempts and
	// DefaultLinkRetryDelay.
	RetryAttempts int
	RetryDelay    time.Duration
}

// Ensure returns the GoTrue user linked to the account, creating and linking
// one when there is none; created tells which. Accounts are matched by their
// link only, never by email or name.
func (u *ExternalUsers) Ensure(ctx context.Context, account ExternalAccount) (userID string, created bool, err error) {
	link, err := u.Links.GetExternalIdentity(account.Source, account.ExternalID)
	if err == nil {
		return link.UserID, false, nil
	}

	if !errors.Is(err, storage.ErrResourceNotFound) {
		return "", false, fmt.Errorf("look up link: %w", err)
	}

	return u.create(ctx, account)
}

func (u *ExternalUsers) create(ctx context.Context, account ExternalAccount) (string, bool, error) {
	created, err := u.Client.AdminCreateUser(types.AdminCreateUserRequest{
		Email:        account.Email,
		EmailConfirm: true,
		UserMetadata: ExternalUserMetadata(account.Username, account.DisplayName, account.SourceEmail),
		AppMetadata:  map[string]any{IdentitySourceKey: account.IdentitySource},
	})
	if err != nil {
		// A concurrent creation of the same account came first, so the
		// placeholder email is taken; its link follows shortly.
		if link, lerr := u.waitForLink(ctx, account.Source, account.ExternalID); lerr == nil {
			return link.UserID, false, nil
		}

		return "", false, fmt.Errorf("create GoTrue user: %w", err)
	}

	userID := created.ID.String()

	err = u.Links.CreateExternalIdentity(&storage.ExternalIdentity{
		Source:     account.Source,
		ExternalID: account.ExternalID,
		UserID:     userID,
		Username:   account.Username,
		Email:      account.SourceEmail,
	})
	if err == nil {
		klog.Infof("%s account %q (id %s): created user %s", account.Source, account.LogName, account.ExternalID, userID)
		return userID, true, nil
	}

	// The user is unusable without its link, and would block the next attempt
	// by holding the placeholder email.
	if derr := u.Client.AdminDeleteUser(types.AdminDeleteUserRequest{UserID: created.ID}); derr != nil {
		klog.Errorf("%s account %q: failed to delete unlinked user %s: %v", account.Source, account.LogName, userID, derr)
	}

	if !errors.Is(err, storage.ErrResourceConflict) {
		return "", false, fmt.Errorf("link user %s: %w", userID, err)
	}

	// Another creation linked the account first; use its user.
	link, err := u.Links.GetExternalIdentity(account.Source, account.ExternalID)
	if err != nil {
		return "", false, fmt.Errorf("look up link after losing the race: %w", err)
	}

	return link.UserID, false, nil
}

func (u *ExternalUsers) waitForLink(ctx context.Context, source, externalID string) (*storage.ExternalIdentity, error) {
	attempts, delay := u.RetryAttempts, u.RetryDelay
	if attempts <= 0 {
		attempts, delay = DefaultLinkRetryAttempts, DefaultLinkRetryDelay
	}

	var err error

	for attempt := 0; attempt < attempts; attempt++ {
		var link *storage.ExternalIdentity

		link, err = u.Links.GetExternalIdentity(source, externalID)
		if err == nil {
			return link, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}

	return nil, err
}

// IsBanned reports whether a GoTrue user is banned at now.
func IsBanned(user *types.User, now time.Time) bool {
	return user != nil && user.BannedUntil != nil && user.BannedUntil.After(now)
}
