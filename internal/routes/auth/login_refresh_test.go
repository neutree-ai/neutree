package auth

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/supabase-community/gotrue-go/types"

	v1 "github.com/neutree-ai/neutree/api/v1"
	internalauth "github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func expectProfile(store *storagemocks.MockStorage, userID, displayName, email string) {
	store.EXPECT().GetUserProfile(userID).Return(&v1.UserProfile{
		Metadata: &v1.Metadata{Name: "profile-name", DisplayName: displayName, Labels: map[string]string{"k": "v"}},
		Spec:     &v1.UserProfileSpec{Email: email},
	}, nil).Once()
}

// A later LDAP login writes the directory's new display name and email to
// the user_metadata keys that changed and to the profile.
func TestLDAPToken_LaterLoginRefreshesProfile(t *testing.T) {
	d := newLDAPTestDeps(t)
	userID := uuid.New()
	id := testIdentity()

	d.storage.EXPECT().GetExternalIdentity(testLDAPLinkSource, id.ExternalID).
		Return(&storage.ExternalIdentity{UserID: userID.String()}, nil).Once()
	d.client.EXPECT().AdminGetUser(types.AdminGetUserRequest{UserID: userID}).
		Return(&types.AdminGetUserResponse{User: types.User{
			ID: userID, Email: testPlaceholderEmail,
			UserMetadata: internalauth.ExternalUserMetadata(id.Username, "Old Name", "old@example.org"),
		}}, nil).Once()
	d.client.EXPECT().AdminUpdateUser(types.AdminUpdateUserRequest{
		UserID:       userID,
		UserMetadata: map[string]any{"name": id.DisplayName, "email": id.Email},
	}).Return(&types.AdminUpdateUserResponse{User: types.User{
		ID: userID, Email: testPlaceholderEmail,
		UserMetadata: internalauth.ExternalUserMetadata(id.Username, id.DisplayName, id.Email),
	}}, nil).Once()
	expectProfile(d.storage, userID.String(), "Old Name", "old@example.org")
	d.storage.EXPECT().UpdateUserProfile(userID.String(), &v1.UserProfile{
		Metadata: &v1.Metadata{Name: "profile-name", DisplayName: id.DisplayName, Labels: map[string]string{"k": "v"}},
		Spec:     &v1.UserProfileSpec{Email: id.Email},
	}).Return(nil).Once()
	d.sessions.EXPECT().GenerateMagicLink(mock.Anything, testPlaceholderEmail).
		Return(&internalauth.MagicLink{UserID: userID.String(), HashedToken: "hash"}, nil).Once()
	d.sessions.EXPECT().VerifyMagicLink(mock.Anything, "hash").Return([]byte(testSession), nil).Once()

	w := d.login(t)

	assert.Equal(t, http.StatusOK, w.Code)
}

// A refresh that fails is logged; the login goes on.
func TestLDAPToken_RefreshFailureDoesNotBlockLogin(t *testing.T) {
	d := newLDAPTestDeps(t)
	userID := uuid.New()

	d.storage.EXPECT().GetExternalIdentity(testLDAPLinkSource, testIdentity().ExternalID).
		Return(&storage.ExternalIdentity{UserID: userID.String()}, nil).Once()
	d.client.EXPECT().AdminGetUser(types.AdminGetUserRequest{UserID: userID}).
		Return(&types.AdminGetUserResponse{User: types.User{ID: userID, Email: testPlaceholderEmail}}, nil).Once()
	d.client.EXPECT().AdminUpdateUser(mock.Anything).Return(nil, errors.New("gotrue down")).Once()
	d.sessions.EXPECT().GenerateMagicLink(mock.Anything, testPlaceholderEmail).
		Return(&internalauth.MagicLink{UserID: userID.String(), HashedToken: "hash"}, nil).Once()
	d.sessions.EXPECT().VerifyMagicLink(mock.Anything, "hash").Return([]byte(testSession), nil).Once()

	w := d.login(t)

	assert.Equal(t, http.StatusOK, w.Code)
	d.storage.AssertNotCalled(t, "UpdateUserProfile", mock.Anything, mock.Anything)
}

// A later OIDC login takes the provider's new display name. A claim the
// provider leaves out (here the email) keeps the stored value.
func TestOIDCCallback_LaterLoginRefreshesProfile(t *testing.T) {
	e := newOIDCTestEnv(t)
	e.idp.Claims = map[string]any{"preferred_username": "kc.alice", "name": "KC Alice Renamed"}
	userID := uuid.New()

	e.storage.EXPECT().GetExternalIdentity(testOIDCLinkSource, e.externalID()).
		Return(&storage.ExternalIdentity{UserID: userID.String()}, nil).Once()
	e.client.EXPECT().AdminGetUser(types.AdminGetUserRequest{UserID: userID}).
		Return(&types.AdminGetUserResponse{User: types.User{
			ID: userID, Email: e.placeholderEmail(),
			UserMetadata: internalauth.ExternalUserMetadata("kc.alice", "KC Alice", "alice@example.org"),
		}}, nil).Once()
	e.client.EXPECT().AdminUpdateUser(types.AdminUpdateUserRequest{
		UserID:       userID,
		UserMetadata: map[string]any{"name": "KC Alice Renamed"},
	}).Return(&types.AdminUpdateUserResponse{User: types.User{
		ID: userID, Email: e.placeholderEmail(),
		UserMetadata: internalauth.ExternalUserMetadata("kc.alice", "KC Alice Renamed", "alice@example.org"),
	}}, nil).Once()
	expectProfile(e.storage, userID.String(), "KC Alice", "alice@example.org")
	e.storage.EXPECT().UpdateUserProfile(userID.String(), &v1.UserProfile{
		Metadata: &v1.Metadata{Name: "profile-name", DisplayName: "KC Alice Renamed", Labels: map[string]string{"k": "v"}},
		Spec:     &v1.UserProfileSpec{Email: "alice@example.org"},
	}).Return(nil).Once()
	e.sessions.EXPECT().GenerateMagicLink(mock.Anything, e.placeholderEmail()).
		Return(&internalauth.MagicLink{UserID: userID.String(), HashedToken: "hashed-token"}, nil).Once()

	_, fragment := redirectFragment(t, e.loginFlow(t))

	assert.Equal(t, url.Values{"token_hash": {"hashed-token"}, "type": {"magiclink"}}, fragment)
}
