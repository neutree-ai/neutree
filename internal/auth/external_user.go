package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// PlaceholderEmail is the GoTrue email of an external account. The external
// ID is case-sensitive and may hold any character, so the local part is its
// hash.
func PlaceholderEmail(sourceName, domain, externalID string) string {
	sum := sha256.Sum256([]byte(externalID))
	return hex.EncodeToString(sum[:])[:32] + "@" + sourceName + "." + domain
}

// LDAPPlaceholderEmail is the GoTrue email of a directory account.
func LDAPPlaceholderEmail(sourceName, externalID string) string {
	return PlaceholderEmail(sourceName, LDAPEmailDomain, externalID)
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
	// IdentitySourceName is the name of the IdentitySource, recorded in the
	// user's app_metadata for display.
	IdentitySourceName string
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

// Profile is what the source says about the account.
func (a ExternalAccount) Profile() ExternalProfile {
	return ExternalProfile{Username: a.Username, DisplayName: a.DisplayName, Email: a.SourceEmail}
}

// LDAPAccount describes a directory account of the LDAP identity source
// sourceName. A login and the organization sync build it the same way, so a
// user created by either is the same user to the other.
func LDAPAccount(sourceName, externalID, username, displayName, email string) ExternalAccount {
	return ExternalAccount{
		Source:             LinkSource(LDAPSource, sourceName),
		ExternalID:         externalID,
		IdentitySource:     LDAPSource,
		IdentitySourceName: sourceName,
		Email:              LDAPPlaceholderEmail(sourceName, externalID),
		Username:           username,
		DisplayName:        displayName,
		SourceEmail:        email,
		LogName:            username,
	}
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

// Ensure returns the link of the account to its GoTrue user, creating and
// linking a user when there is none; created tells which. Accounts are
// matched by their link only, never by email or name.
func (u *ExternalUsers) Ensure(ctx context.Context, account ExternalAccount) (link *storage.ExternalIdentity, created bool, err error) {
	link, err = u.Links.GetExternalIdentity(account.Source, account.ExternalID)
	if err == nil {
		return link, false, nil
	}

	if !errors.Is(err, storage.ErrResourceNotFound) {
		return nil, false, fmt.Errorf("look up link: %w", err)
	}

	return u.create(ctx, account)
}

func (u *ExternalUsers) create(ctx context.Context, account ExternalAccount) (*storage.ExternalIdentity, bool, error) {
	created, err := u.Client.AdminCreateUser(types.AdminCreateUserRequest{
		Email:        account.Email,
		EmailConfirm: true,
		UserMetadata: ExternalUserMetadata(account.Username, account.DisplayName, account.SourceEmail),
		AppMetadata:  ExternalAppMetadata(account.IdentitySource, account.IdentitySourceName),
	})
	if err != nil {
		// A concurrent creation of the same account came first, so the
		// placeholder email is taken; its link follows shortly.
		if link, lerr := u.waitForLink(ctx, account.Source, account.ExternalID); lerr == nil {
			return link, false, nil
		}

		return nil, false, fmt.Errorf("create GoTrue user: %w", err)
	}

	userID := created.ID.String()

	link := &storage.ExternalIdentity{
		Source:     account.Source,
		ExternalID: account.ExternalID,
		UserID:     userID,
		Username:   account.Username,
		Email:      account.SourceEmail,
	}

	err = u.Links.CreateExternalIdentity(link)
	if err == nil {
		klog.Infof("%s account %q (id %s): created user %s", account.Source, account.LogName, account.ExternalID, userID)
		return link, true, nil
	}

	// The user is unusable without its link, and would block the next attempt
	// by holding the placeholder email.
	if derr := u.Client.AdminDeleteUser(types.AdminDeleteUserRequest{UserID: created.ID}); derr != nil {
		klog.Errorf("%s account %q: failed to delete unlinked user %s: %v", account.Source, account.LogName, userID, derr)
	}

	if !errors.Is(err, storage.ErrResourceConflict) {
		return nil, false, fmt.Errorf("link user %s: %w", userID, err)
	}

	// Another creation linked the account first; use its user.
	link, err = u.Links.GetExternalIdentity(account.Source, account.ExternalID)
	if err != nil {
		return nil, false, fmt.Errorf("look up link after losing the race: %w", err)
	}

	return link, false, nil
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
