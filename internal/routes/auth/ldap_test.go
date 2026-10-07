package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/supabase-community/gotrue-go/types"

	internalauth "github.com/neutree-ai/neutree/internal/auth"
	authmocks "github.com/neutree-ai/neutree/internal/auth/mocks"
	"github.com/neutree-ai/neutree/pkg/identity/ldap"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

const testSession = `{"access_token":"at","token_type":"bearer","expires_in":3600,"expires_at":1,"refresh_token":"rt","user":{"id":"x"}}`

type fakeLDAP struct {
	identity *ldap.Identity
	err      error
}

func (f *fakeLDAP) Authenticate(_ context.Context, _, _ string) (*ldap.Identity, error) {
	return f.identity, f.err
}

type ldapTestDeps struct {
	storage  *storagemocks.MockStorage
	client   *authmocks.MockClient
	sessions *authmocks.MockSessionIssuer
	ldap     *fakeLDAP
}

func newLDAPTestDeps(t *testing.T) *ldapTestDeps {
	t.Helper()

	attempts, delay := linkRetryAttempts, linkRetryDelay
	linkRetryAttempts, linkRetryDelay = 2, 0

	t.Cleanup(func() { linkRetryAttempts, linkRetryDelay = attempts, delay })

	return &ldapTestDeps{
		storage:  storagemocks.NewMockStorage(t),
		client:   authmocks.NewMockClient(t),
		sessions: authmocks.NewMockSessionIssuer(t),
		ldap:     &fakeLDAP{identity: testIdentity()},
	}
}

func testIdentity() *ldap.Identity {
	return &ldap.Identity{
		ExternalID:  "8F6C0E5E-1B0B-4A4B-9C1D-2F3E4D5C6B7A",
		DN:          "uid=alice,ou=people,dc=example,dc=org",
		Username:    "alice",
		Email:       "alice@example.org",
		DisplayName: "Alice Liddell",
	}
}

const testPlaceholderEmail = "8f6c0e5e-1b0b-4a4b-9c1d-2f3e4d5c6b7a@ldap.neutree.local"

func (d *ldapTestDeps) serve(t *testing.T, ldapEnabled bool, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)

	deps := &Dependencies{
		AuthEndpoint: "http://gotrue.invalid",
		Storage:      d.storage,
		AuthClient:   d.client,
	}
	if ldapEnabled {
		deps.LDAP = d.ldap
		deps.Sessions = d.sessions
	}

	engine := gin.New()
	RegisterAuthRoutes(engine.Group("/api/v1"), nil, deps)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)

	return w
}

func (d *ldapTestDeps) login(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	return d.serve(t, true, http.MethodPost, "/api/v1/auth/ldap/token", `{"username":"alice","password":"pw"}`)
}

func (d *ldapTestDeps) expectSession(userID string) {
	d.sessions.EXPECT().GenerateMagicLink(mock.Anything, testPlaceholderEmail).
		Return(&internalauth.MagicLink{UserID: userID, HashedToken: "hash"}, nil).Once()
	d.sessions.EXPECT().VerifyMagicLink(mock.Anything, "hash").Return([]byte(testSession), nil).Once()
}

func errorBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	return body.Error
}

func TestLDAPToken_FirstLoginCreatesUserAndLink(t *testing.T) {
	d := newLDAPTestDeps(t)
	userID := uuid.New()

	d.storage.EXPECT().GetExternalIdentity("ldap", testIdentity().ExternalID).Return(nil, storage.ErrResourceNotFound).Once()
	d.client.EXPECT().AdminCreateUser(mock.MatchedBy(func(req types.AdminCreateUserRequest) bool {
		_, hasUsername := req.UserMetadata["username"]

		return req.Email == testPlaceholderEmail &&
			req.EmailConfirm &&
			req.Password == nil &&
			!hasUsername &&
			req.UserMetadata["preferred_username"] == "alice" &&
			req.UserMetadata["name"] == "Alice Liddell" &&
			req.UserMetadata["email"] == "alice@example.org" &&
			req.AppMetadata["identity_source"] == "ldap"
	})).Return(&types.AdminCreateUserResponse{User: types.User{ID: userID}}, nil).Once()
	d.storage.EXPECT().CreateExternalIdentity(&storage.ExternalIdentity{
		Source: "ldap", ExternalID: testIdentity().ExternalID, UserID: userID.String(),
	}).Return(nil).Once()
	d.expectSession(userID.String())

	w := d.login(t)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, testSession, w.Body.String())
}

func TestLDAPToken_LaterLoginUsesLink(t *testing.T) {
	d := newLDAPTestDeps(t)
	userID := uuid.NewString()

	d.storage.EXPECT().GetExternalIdentity("ldap", testIdentity().ExternalID).
		Return(&storage.ExternalIdentity{Source: "ldap", ExternalID: testIdentity().ExternalID, UserID: userID}, nil).Once()
	d.expectSession(userID)

	w := d.login(t)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, testSession, w.Body.String())
	d.client.AssertNotCalled(t, "AdminCreateUser", mock.Anything)
}

func TestLDAPToken_MagicLinkForAnotherUserFailsClosed(t *testing.T) {
	d := newLDAPTestDeps(t)

	d.storage.EXPECT().GetExternalIdentity("ldap", testIdentity().ExternalID).
		Return(&storage.ExternalIdentity{UserID: uuid.NewString()}, nil).Once()
	d.sessions.EXPECT().GenerateMagicLink(mock.Anything, testPlaceholderEmail).
		Return(&internalauth.MagicLink{UserID: uuid.NewString(), HashedToken: "hash"}, nil).Once()

	w := d.login(t)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "access_token")
	d.sessions.AssertNotCalled(t, "VerifyMagicLink", mock.Anything, mock.Anything)
}

func TestLDAPToken_AuthenticateErrors(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{"invalid credentials", ldap.ErrInvalidCredentials, http.StatusUnauthorized, msgInvalidCredentials},
		{"user not found", ldap.ErrUserNotFound, http.StatusUnauthorized, msgInvalidCredentials},
		{"empty username", ldap.ErrEmptyUsername, http.StatusUnauthorized, msgInvalidCredentials},
		{"empty password", ldap.ErrEmptyPassword, http.StatusUnauthorized, msgInvalidCredentials},
		{"multiple users", ldap.ErrMultipleUsers, http.StatusUnauthorized, msgInvalidCredentials},
		{"connection", fmt.Errorf("%w: dial: refused", ldap.ErrConnection), http.StatusServiceUnavailable, msgDirectoryDown},
		{"tls", ldap.ErrTLS, http.StatusServiceUnavailable, msgDirectoryDown},
		{"service bind", ldap.ErrServiceBindFailed, http.StatusServiceUnavailable, msgDirectoryDown},
		{"missing attribute", ldap.ErrMissingAttribute, http.StatusInternalServerError, msgInternalError},
		{"unknown", errors.New("boom"), http.StatusInternalServerError, msgInternalError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newLDAPTestDeps(t)
			d.ldap.identity, d.ldap.err = nil, tc.err

			w := d.login(t)

			assert.Equal(t, tc.status, w.Code)
			assert.Equal(t, tc.message, errorBody(t, w))
		})
	}
}

func TestLDAPToken_BadBody(t *testing.T) {
	d := newLDAPTestDeps(t)

	w := d.serve(t, true, http.MethodPost, "/api/v1/auth/ldap/token", `not json`)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestLDAPToken_LinkInsertLosesRace(t *testing.T) {
	d := newLDAPTestDeps(t)
	orphan := uuid.New()
	winner := uuid.NewString()

	d.storage.EXPECT().GetExternalIdentity("ldap", testIdentity().ExternalID).Return(nil, storage.ErrResourceNotFound).Once()
	d.client.EXPECT().AdminCreateUser(mock.Anything).Return(&types.AdminCreateUserResponse{User: types.User{ID: orphan}}, nil).Once()
	d.storage.EXPECT().CreateExternalIdentity(mock.Anything).
		Return(fmt.Errorf("(23505) duplicate key: %w", storage.ErrResourceConflict)).Once()
	d.client.EXPECT().AdminDeleteUser(types.AdminDeleteUserRequest{UserID: orphan}).Return(nil).Once()
	d.storage.EXPECT().GetExternalIdentity("ldap", testIdentity().ExternalID).
		Return(&storage.ExternalIdentity{UserID: winner}, nil).Once()
	d.expectSession(winner)

	w := d.login(t)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestLDAPToken_LinkInsertFailsDeletesOrphan(t *testing.T) {
	d := newLDAPTestDeps(t)
	orphan := uuid.New()

	d.storage.EXPECT().GetExternalIdentity("ldap", testIdentity().ExternalID).Return(nil, storage.ErrResourceNotFound).Once()
	d.client.EXPECT().AdminCreateUser(mock.Anything).Return(&types.AdminCreateUserResponse{User: types.User{ID: orphan}}, nil).Once()
	d.storage.EXPECT().CreateExternalIdentity(mock.Anything).Return(errors.New("postgrest down")).Once()
	d.client.EXPECT().AdminDeleteUser(types.AdminDeleteUserRequest{UserID: orphan}).Return(nil).Once()

	w := d.login(t)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestLDAPToken_CreateUserLosesRace(t *testing.T) {
	d := newLDAPTestDeps(t)
	winner := uuid.NewString()

	// The concurrent login holds the placeholder email, then links it.
	d.storage.EXPECT().GetExternalIdentity("ldap", testIdentity().ExternalID).Return(nil, storage.ErrResourceNotFound).Twice()
	d.client.EXPECT().AdminCreateUser(mock.Anything).Return(nil, errors.New("422: email_exists")).Once()
	d.storage.EXPECT().GetExternalIdentity("ldap", testIdentity().ExternalID).
		Return(&storage.ExternalIdentity{UserID: winner}, nil).Once()
	d.expectSession(winner)

	w := d.login(t)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestLDAPToken_CreateUserFails(t *testing.T) {
	d := newLDAPTestDeps(t)

	d.storage.EXPECT().GetExternalIdentity("ldap", testIdentity().ExternalID).Return(nil, storage.ErrResourceNotFound)
	d.client.EXPECT().AdminCreateUser(mock.Anything).Return(nil, errors.New("gotrue down")).Once()

	w := d.login(t)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	d.sessions.AssertNotCalled(t, "GenerateMagicLink", mock.Anything, mock.Anything)
}

func TestAuthRoutes_Registration(t *testing.T) {
	cases := []struct {
		name        string
		ldapEnabled bool
		path        string
		wantFound   bool
	}{
		{"ldap route absent without LDAP", false, "/api/v1/auth/ldap/token", false},
		{"ldap route present with LDAP", true, "/api/v1/auth/ldap/token", true},
		{"signup route gone", false, "/api/v1/auth/signup", false},
		{"signup route gone with LDAP", true, "/api/v1/auth/signup", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newLDAPTestDeps(t)
			// A found LDAP route answers 400 to a malformed body before touching
			// any dependency.
			w := d.serve(t, tc.ldapEnabled, http.MethodPost, tc.path, `not json`)

			if tc.wantFound {
				assert.Equal(t, http.StatusBadRequest, w.Code)
			} else {
				assert.Equal(t, http.StatusNotFound, w.Code)
			}
		})
	}
}
