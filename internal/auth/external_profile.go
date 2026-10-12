package auth

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/supabase-community/gotrue-go/types"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// ExternalProfile is what an identity source says about an account: its
// username, display name and email there.
type ExternalProfile struct {
	Username    string
	DisplayName string
	Email       string
}

// externalUserMetadataKeys are the user_metadata keys ExternalUserMetadata
// may set.
var externalUserMetadataKeys = []string{"preferred_username", "name", "email"}

// ExternalUserMetadataKeys returns the user_metadata keys the identity source
// owns for an external user, which the user must not change.
func ExternalUserMetadataKeys() []string {
	return slices.Clone(externalUserMetadataKeys)
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

// ExternalUserStore is the storage UpdateExternalUser writes; storage.Storage
// implements it.
type ExternalUserStore interface {
	GetUserProfile(id string) (*v1.UserProfile, error)
	UpdateUserProfile(id string, data *v1.UserProfile) error
	UpdateExternalIdentityAccount(data *storage.ExternalIdentity) error
}

// ExternalUserUpdate is what UpdateExternalUser brings in line.
type ExternalUserUpdate struct {
	// Link is the user's link as stored: its source, external ID, user ID,
	// and the username and email last recorded on it.
	Link *storage.ExternalIdentity
	// User is the GoTrue user as it is now. When it is given, only the
	// user_metadata keys that differ are written, and nothing when none
	// does; when it is nil, all keys are written.
	User *types.User
	// Profile is what the identity source says now.
	Profile ExternalProfile
	// ClearMissing removes a value the source leaves empty (the
	// organization sync reads the whole directory entry, so empty means
	// gone). Without it the stored value is kept (a login sees only what the
	// source chose to send).
	ClearMissing bool
}

// UpdateExternalUser brings an external user in line with what its identity
// source says now (the source owns the display name and email): the
// GoTrue user_metadata, the profile's display name and email, and the
// username and email recorded on the link. Each is written only when it
// differs. The organization sync and every LDAP or OIDC login use it.
//
// The profile email can only be changed to what the database derives for the
// user (migration 108): user_metadata.email, or the GoTrue email when there is
// none. So user_metadata is written first, and the profile follows it.
func UpdateExternalUser(client Client, store ExternalUserStore, u ExternalUserUpdate) error {
	userID := u.Link.UserID
	p := u.Profile
	want := ExternalUserMetadata(p.Username, p.DisplayName, p.Email)

	var metadata map[string]any

	gotrueEmail := ""

	if u.User != nil {
		metadata = u.User.UserMetadata
		gotrueEmail = u.User.Email
	}

	update := map[string]any{}

	for _, key := range externalUserMetadataKeys {
		value, ok := want[key]
		if !ok {
			if !u.ClearMissing {
				continue
			}

			// A key set to null is removed from user_metadata.
			value = nil
		}

		if u.User != nil && sameMetadataValue(metadata[key], value) {
			continue
		}

		update[key] = value
	}

	if len(update) > 0 {
		id, err := uuid.Parse(userID)
		if err != nil {
			return fmt.Errorf("invalid user ID %q: %w", userID, err)
		}

		updated, err := client.AdminUpdateUser(types.AdminUpdateUserRequest{UserID: id, UserMetadata: update})
		if err != nil {
			return fmt.Errorf("update GoTrue user: %w", err)
		}

		if updated != nil {
			metadata = updated.UserMetadata
			gotrueEmail = updated.Email
		}
	}

	email, _ := metadata["email"].(string)
	if email == "" {
		email = gotrueEmail
	}

	if err := updateExternalProfile(store, userID, p.DisplayName, email); err != nil {
		return err
	}

	return updateLinkAccount(store, u.Link, p, u.ClearMissing)
}

// updateLinkAccount records the source's username and email on the link.
func updateLinkAccount(store ExternalUserStore, link *storage.ExternalIdentity, p ExternalProfile, clearMissing bool) error {
	username, email := p.Username, p.Email

	if !clearMissing {
		if username == "" {
			username = link.Username
		}

		if email == "" {
			email = link.Email
		}
	}

	if username == link.Username && email == link.Email {
		return nil
	}

	if err := store.UpdateExternalIdentityAccount(&storage.ExternalIdentity{
		Source:     link.Source,
		ExternalID: link.ExternalID,
		Username:   username,
		Email:      email,
	}); err != nil {
		return fmt.Errorf("update link: %w", err)
	}

	link.Username, link.Email = username, email

	return nil
}

// updateExternalProfile sets the profile's display name and email; an empty
// value keeps the stored one.
func updateExternalProfile(profiles ExternalUserStore, userID, displayName, email string) error {
	profile, err := profiles.GetUserProfile(userID)
	if err != nil {
		return fmt.Errorf("get profile: %w", err)
	}

	if profile.Metadata == nil {
		return errors.New("profile has no metadata")
	}

	stored := ""
	if profile.Spec != nil {
		stored = profile.Spec.Email
	}

	if displayName == "" {
		displayName = profile.Metadata.DisplayName
	}

	if email == "" {
		email = stored
	}

	if profile.Metadata.DisplayName == displayName && stored == email {
		return nil
	}

	// metadata and spec are composites PostgREST replaces whole: send the
	// stored metadata with the new display name, and all of spec.
	metadata := *profile.Metadata
	metadata.DisplayName = displayName

	if err := profiles.UpdateUserProfile(userID, &v1.UserProfile{
		Metadata: &metadata,
		Spec:     &v1.UserProfileSpec{Email: email},
	}); err != nil {
		return fmt.Errorf("update profile: %w", err)
	}

	return nil
}

func sameMetadataValue(stored, want any) bool {
	if want == nil {
		return stored == nil
	}

	s, ok := stored.(string)

	return ok && s == want
}

// RecordIdentitySourceName sets app_metadata.identity_source_name of user to
// name when it differs. Users created before the key existed lack it.
func RecordIdentitySourceName(client Client, user *types.User, name string) error {
	if current, _ := user.AppMetadata[IdentitySourceNameKey].(string); current == name {
		return nil
	}

	// GoTrue merges the given keys into app_metadata.
	if _, err := client.AdminUpdateUser(types.AdminUpdateUserRequest{
		UserID:      user.ID,
		AppMetadata: map[string]any{IdentitySourceNameKey: name},
	}); err != nil {
		return fmt.Errorf("update GoTrue app_metadata: %w", err)
	}

	return nil
}

// IsBanned reports whether a GoTrue user is banned at now.
func IsBanned(user *types.User, now time.Time) bool {
	return user != nil && user.BannedUntil != nil && user.BannedUntil.After(now)
}
