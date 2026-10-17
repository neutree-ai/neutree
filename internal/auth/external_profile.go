package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/supabase-community/gotrue-go/types"

	v1 "github.com/neutree-ai/neutree/api/v1"
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

// UserProfileStore reads and writes user profiles; storage.Storage
// implements it.
type UserProfileStore interface {
	GetUserProfile(id string) (*v1.UserProfile, error)
	UpdateUserProfile(id string, data *v1.UserProfile) error
}

// UpdateExternalUser brings the GoTrue user_metadata and the profile of the
// external user userID in line with what its identity source says now
// (decision D56: the source owns the display name and email).
//
// current is the user as GoTrue has it. When it is given, only the
// user_metadata keys that differ are written, and nothing when none does;
// when it is nil, all keys are written.
//
// An empty value in p means the source said nothing about it. With
// clearMissing the stored value is removed (the organization sync reads the
// whole directory entry, so empty means gone); otherwise it is kept (a login
// sees only what the source chose to send).
//
// The profile email can only be changed to what the database derives for the
// user (migration 106): user_metadata.email, or the GoTrue email when there is
// none. So user_metadata is written first, and the profile follows it. The
// profile is written only when its display name or email differs.
func UpdateExternalUser(client Client, profiles UserProfileStore, userID string, current *types.User, p ExternalProfile, clearMissing bool) error {
	want := ExternalUserMetadata(p.Username, p.DisplayName, p.Email)

	var metadata map[string]any

	gotrueEmail := ""

	if current != nil {
		metadata = current.UserMetadata
		gotrueEmail = current.Email
	}

	update := map[string]any{}

	for _, key := range externalUserMetadataKeys {
		value, ok := want[key]
		if !ok {
			if !clearMissing {
				continue
			}

			// A key set to null is removed from user_metadata.
			value = nil
		}

		if current != nil && sameMetadataValue(metadata[key], value) {
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

	return updateExternalProfile(profiles, userID, p.DisplayName, email)
}

// updateExternalProfile sets the profile's display name and email; an empty
// value keeps the stored one.
func updateExternalProfile(profiles UserProfileStore, userID, displayName, email string) error {
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
