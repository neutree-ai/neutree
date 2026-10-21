package auth

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/supabase-community/gotrue-go/types"

	internalauth "github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// A user banned in GoTrue is refused, with the credentials checked first.
func TestLDAPToken_BannedUserIsRefused(t *testing.T) {
	d := newLDAPTestDeps(t)
	userID := uuid.New()
	until := time.Now().Add(time.Hour)

	d.storage.EXPECT().GetExternalIdentity(testLDAPLinkSource, testIdentity().ExternalID).
		Return(&storage.ExternalIdentity{Source: testLDAPLinkSource, ExternalID: testIdentity().ExternalID, UserID: userID.String()}, nil).Once()
	d.client.EXPECT().AdminGetUser(types.AdminGetUserRequest{UserID: userID}).
		Return(&types.AdminGetUserResponse{User: types.User{ID: userID, Email: testPlaceholderEmail, BannedUntil: &until}}, nil).Once()

	w := d.login(t)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, msgUserDisabled, errorBody(t, w))
	d.sessions.AssertNotCalled(t, "GenerateMagicLink", mock.Anything, mock.Anything)
	d.client.AssertNotCalled(t, "AdminUpdateUser", mock.Anything)
}

// A ban that has run out does not block the login.
func TestLDAPToken_ExpiredBanAllowsLogin(t *testing.T) {
	d := newLDAPTestDeps(t)
	userID := uuid.New()
	until := time.Now().Add(-time.Hour)
	id := testIdentity()

	d.storage.EXPECT().GetExternalIdentity(testLDAPLinkSource, id.ExternalID).
		Return(&storage.ExternalIdentity{Source: testLDAPLinkSource, ExternalID: id.ExternalID, UserID: userID.String()}, nil).Once()
	d.client.EXPECT().AdminGetUser(types.AdminGetUserRequest{UserID: userID}).
		Return(&types.AdminGetUserResponse{User: types.User{
			ID: userID, Email: testPlaceholderEmail, BannedUntil: &until,
			UserMetadata: internalauth.ExternalUserMetadata(id.Username, id.DisplayName, id.Email),
			AppMetadata:  internalauth.ExternalAppMetadata(internalauth.LDAPSource, testLDAPSource),
		}}, nil).Once()
	expectProfile(d.storage, userID.String(), id.DisplayName, id.Email)
	d.sessions.EXPECT().GenerateMagicLink(mock.Anything, testPlaceholderEmail).
		Return(&internalauth.MagicLink{UserID: userID.String(), HashedToken: "hash"}, nil).Once()
	d.sessions.EXPECT().VerifyMagicLink(mock.Anything, "hash").Return([]byte(testSession), nil).Once()

	w := d.login(t)

	assert.Equal(t, http.StatusOK, w.Code)
}
