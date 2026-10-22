package auth

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/supabase-community/gotrue-go/types"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type fakeProfiles struct {
	profile *v1.UserProfile
	updates []*v1.UserProfile
}

func (f *fakeProfiles) GetUserProfile(string) (*v1.UserProfile, error) { return f.profile, nil }

func (f *fakeProfiles) UpdateUserProfile(_ string, data *v1.UserProfile) error {
	f.updates = append(f.updates, data)
	return nil
}

type fakeClient struct {
	Client
	user    types.User
	updates []types.AdminUpdateUserRequest
}

func (f *fakeClient) AdminUpdateUser(req types.AdminUpdateUserRequest) (*types.AdminUpdateUserResponse, error) {
	f.updates = append(f.updates, req)

	if f.user.UserMetadata == nil {
		f.user.UserMetadata = map[string]any{}
	}

	for k, v := range req.UserMetadata {
		if v == nil {
			delete(f.user.UserMetadata, k)
		} else {
			f.user.UserMetadata[k] = v
		}
	}

	return &types.AdminUpdateUserResponse{User: f.user}, nil
}

func profileOf(displayName, email string) *v1.UserProfile {
	return &v1.UserProfile{
		Metadata: &v1.Metadata{Name: "alice", DisplayName: displayName},
		Spec:     &v1.UserProfileSpec{Email: email},
	}
}

func TestUpdateExternalUser_NothingChangedWritesNothing(t *testing.T) {
	user := types.User{ID: uuid.New(), Email: "x@corp.ldap.neutree.local",
		UserMetadata: ExternalUserMetadata("alice", "Alice", "alice@example.org")}
	client := &fakeClient{user: user}
	profiles := &fakeProfiles{profile: profileOf("Alice", "alice@example.org")}

	err := UpdateExternalUser(client, profiles, user.ID.String(), &user,
		ExternalProfile{Username: "alice", DisplayName: "Alice", Email: "alice@example.org"}, false)

	require.NoError(t, err)
	assert.Empty(t, client.updates)
	assert.Empty(t, profiles.updates)
}

// Without clearMissing, a value the source leaves empty keeps what is stored.
func TestUpdateExternalUser_EmptyKeepsStored(t *testing.T) {
	user := types.User{ID: uuid.New(), Email: "x@corp.oidc.neutree.local",
		UserMetadata: ExternalUserMetadata("alice", "Alice", "alice@example.org")}
	client := &fakeClient{user: user}
	profiles := &fakeProfiles{profile: profileOf("Alice", "alice@example.org")}

	err := UpdateExternalUser(client, profiles, user.ID.String(), &user, ExternalProfile{Username: "alice"}, false)

	require.NoError(t, err)
	assert.Empty(t, client.updates)
	assert.Empty(t, profiles.updates)
}

// With clearMissing and no current user (the organization sync), every key is
// written and a missing email falls back to the GoTrue email.
func TestUpdateExternalUser_ClearMissing(t *testing.T) {
	id := uuid.New()
	client := &fakeClient{user: types.User{ID: id, Email: "x@corp.ldap.neutree.local",
		UserMetadata: ExternalUserMetadata("alice", "Alice", "alice@example.org")}}
	profiles := &fakeProfiles{profile: profileOf("Alice", "alice@example.org")}

	err := UpdateExternalUser(client, profiles, id.String(), nil,
		ExternalProfile{Username: "alice", DisplayName: "Alice L"}, true)

	require.NoError(t, err)
	require.Len(t, client.updates, 1)
	assert.Equal(t, map[string]any{"preferred_username": "alice", "name": "Alice L", "email": nil}, client.updates[0].UserMetadata)
	require.Len(t, profiles.updates, 1)
	assert.Equal(t, "Alice L", profiles.updates[0].Metadata.DisplayName)
	assert.Equal(t, "x@corp.ldap.neutree.local", profiles.updates[0].Spec.Email)
}

// Only the keys that differ are written.
func TestUpdateExternalUser_WritesChangedKeysOnly(t *testing.T) {
	user := types.User{ID: uuid.New(), Email: "x@corp.ldap.neutree.local",
		UserMetadata: ExternalUserMetadata("alice", "Alice", "alice@example.org")}
	client := &fakeClient{user: user}
	profiles := &fakeProfiles{profile: profileOf("Alice", "alice@example.org")}

	err := UpdateExternalUser(client, profiles, user.ID.String(), &user,
		ExternalProfile{Username: "alice", DisplayName: "Alice", Email: "alice@new.example.org"}, false)

	require.NoError(t, err)
	require.Len(t, client.updates, 1)
	assert.Equal(t, map[string]any{"email": "alice@new.example.org"}, client.updates[0].UserMetadata)
	require.Len(t, profiles.updates, 1)
	assert.Equal(t, "Alice", profiles.updates[0].Metadata.DisplayName)
	assert.Equal(t, "alice@new.example.org", profiles.updates[0].Spec.Email)
}
