package dbtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// These tests cover api.external_identities (migration 102), which links a
// directory account to the GoTrue user an LDAP login created for it, and the
// GoTrue calls the LDAP login turns that user into a session with.

// createLDAPShapedUser creates a user the way the LDAP login does: placeholder
// email, no password. It is not deleted automatically, so tests can delete it
// themselves to check the cascade.
func createLDAPShapedUser(t *testing.T, externalID string) string {
	t.Helper()

	code, raw := gotrueRequest(t, http.MethodPost, "/admin/users", gotrueServiceToken(t), map[string]any{
		"email":         externalID + "@ldap.neutree.local",
		"email_confirm": true,
		"user_metadata": map[string]any{"preferred_username": "ldap-" + externalID, "name": "LDAP User"},
		"app_metadata":  map[string]any{"identity_source": "ldap"},
	})
	if code != http.StatusOK {
		t.Fatalf("POST /admin/users = HTTP %d, want 200: %s", code, raw)
	}

	var user struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &user); err != nil || user.ID == "" {
		t.Fatalf("failed to decode the created user %q: %v", raw, err)
	}

	return user.ID
}

func deleteGoTrueUser(t *testing.T, id string) {
	t.Helper()

	if code, raw := gotrueRequest(t, http.MethodDelete, "/admin/users/"+id, gotrueServiceToken(t), nil); code != http.StatusOK {
		t.Errorf("failed to delete user %s: HTTP %d: %s", id, code, raw)
	}
}

func newExternalID() string {
	return fmt.Sprintf("ext-%d", time.Now().UnixNano())
}

func TestExternalIdentities_Constraints(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	extID := newExternalID()
	userID := createLDAPShapedUser(t, extID)
	t.Cleanup(func() { deleteGoTrueUser(t, userID) })

	insert := func(source, externalID, user string) error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO api.external_identities (source, external_id, user_id) VALUES ($1, $2, $3)`,
			source, externalID, user)
		return err
	}

	if err := insert("ldap", extID, userID); err != nil {
		t.Fatalf("first link failed: %v", err)
	}

	cases := []struct {
		name       string
		source     string
		externalID string
		userID     string
		wantCode   pq.ErrorCode
	}{
		{"same account linked twice", "ldap", extID, userID, "23505"},
		{"empty source", "", extID + "-x", userID, "23514"},
		{"empty external id", "ldap", "", userID, "23514"},
		{"unknown user", "ldap", extID + "-y", "00000000-0000-0000-0000-000000000001", "23503"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := insert(tc.source, tc.externalID, tc.userID)

			var pqErr *pq.Error
			if !errors.As(err, &pqErr) || pqErr.Code != tc.wantCode {
				t.Errorf("insert error = %v, want SQLSTATE %s", err, tc.wantCode)
			}
		})
	}

	t.Run("same external id from another source", func(t *testing.T) {
		if err := insert("ldap-other", extID, userID); err != nil {
			t.Errorf("insert failed: %v", err)
		}
	})
}

func TestExternalIdentities_CascadeOnUserDelete(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	extID := newExternalID()
	userID := createLDAPShapedUser(t, extID)

	if _, err := db.ExecContext(ctx,
		`INSERT INTO api.external_identities (source, external_id, user_id) VALUES ('ldap', $1, $2)`, extID, userID); err != nil {
		t.Fatalf("link failed: %v", err)
	}

	deleteGoTrueUser(t, userID)

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM api.external_identities WHERE user_id = $1`, userID).Scan(&count); err != nil {
		t.Fatalf("count failed: %v", err)
	}

	if count != 0 {
		t.Errorf("%d links left after the user was deleted, want 0", count)
	}
}

// TestExternalIdentities_ServiceRoleStorage checks the storage methods
// neutree-api uses, through PostgREST with the service-role token.
func TestExternalIdentities_ServiceRoleStorage(t *testing.T) {
	s := NewTestStorage(t)

	extID := newExternalID()
	userID := createLDAPShapedUser(t, extID)
	t.Cleanup(func() { deleteGoTrueUser(t, userID) })

	if _, err := s.GetExternalIdentity("ldap", extID); !errors.Is(err, storage.ErrResourceNotFound) {
		t.Fatalf("get before link = %v, want ErrResourceNotFound", err)
	}

	link := &storage.ExternalIdentity{Source: "ldap", ExternalID: extID, UserID: userID}
	if err := s.CreateExternalIdentity(link); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	got, err := s.GetExternalIdentity("ldap", extID)
	if err != nil || *got != *link {
		t.Fatalf("get = %+v, %v; want %+v", got, err, link)
	}

	if err := s.CreateExternalIdentity(link); !errors.Is(err, storage.ErrResourceConflict) {
		t.Errorf("second create = %v, want ErrResourceConflict", err)
	}
}

// TestExternalIdentities_UserAccessDenied checks a user's own session cannot
// read or write links, neither through PostgREST nor as api_user in SQL.
func TestExternalIdentities_UserAccessDenied(t *testing.T) {
	waitForPostgREST(t)

	db := GetTestDB(t)
	ctx := context.Background()

	extID := newExternalID()
	ldapUserID := createLDAPShapedUser(t, extID)
	t.Cleanup(func() { deleteGoTrueUser(t, ldapUserID) })

	if _, err := db.ExecContext(ctx,
		`INSERT INTO api.external_identities (source, external_id, user_id) VALUES ('ldap', $1, $2)`, extID, ldapUserID); err != nil {
		t.Fatalf("link failed: %v", err)
	}

	user := createGoTrueTestUser(t, "ext-identity-reader", "testpassword")
	session := gotruePasswordLogin(t, user.Email, "testpassword")

	postgrest := func(method, body string) (int, string) {
		req, err := http.NewRequest(method, GetPostgRESTURL()+"/external_identities", strings.NewReader(body))
		if err != nil {
			t.Fatalf("failed to build the request: %v", err)
		}

		req.Header.Set("Accept-Profile", "api")
		req.Header.Set("Content-Profile", "api")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+session.AccessToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		raw, _ := io.ReadAll(resp.Body)

		return resp.StatusCode, string(raw)
	}

	t.Run("PostgREST read", func(t *testing.T) {
		if code, body := postgrest(http.MethodGet, ""); code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("GET /external_identities = HTTP %d, want 401/403: %s", code, body)
		}
	})

	t.Run("PostgREST insert", func(t *testing.T) {
		body := fmt.Sprintf(`{"source":"ldap","external_id":%q,"user_id":%q}`, newExternalID(), user.ID)
		if code, resp := postgrest(http.MethodPost, body); code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("POST /external_identities = HTTP %d, want 401/403: %s", code, resp)
		}
	})

	for _, role := range []string{"api_user", "anonymous"} {
		t.Run("SQL as "+role, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin failed: %v", err)
			}
			defer func() { _ = tx.Rollback() }()

			if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+role); err != nil {
				t.Fatalf("set role failed: %v", err)
			}

			_, err = tx.ExecContext(ctx, `SELECT 1 FROM api.external_identities`)

			var pqErr *pq.Error
			if !errors.As(err, &pqErr) || pqErr.Code != "42501" {
				t.Errorf("select as %s = %v, want insufficient_privilege", role, err)
			}
		})
	}
}

// TestGoTrue_LDAPSessionFlow pins the GoTrue behaviour the LDAP login builds on:
// an admin magic link for a password-less user verifies into a full session,
// and a magic link for an unknown email silently signs up a new user, which is
// why the login compares the link's user with the linked one.
func TestGoTrue_LDAPSessionFlow(t *testing.T) {
	issuer := auth.NewSessionIssuer(GetGoTrueURL(), gotrueServiceToken(t))
	ctx := context.Background()

	t.Run("magic link verifies into a session for the same user", func(t *testing.T) {
		extID := newExternalID()
		userID := createLDAPShapedUser(t, extID)
		t.Cleanup(func() { deleteGoTrueUser(t, userID) })

		link, err := issuer.GenerateMagicLink(ctx, extID+"@ldap.neutree.local")
		if err != nil {
			t.Fatalf("generate_link failed: %v", err)
		}

		if link.UserID != userID {
			t.Fatalf("generate_link user = %s, want %s", link.UserID, userID)
		}

		raw, err := issuer.VerifyMagicLink(ctx, link.HashedToken)
		if err != nil {
			t.Fatalf("verify failed: %v", err)
		}

		var session struct {
			AccessToken  string `json:"access_token"`
			TokenType    string `json:"token_type"`
			ExpiresIn    int    `json:"expires_in"`
			ExpiresAt    int64  `json:"expires_at"`
			RefreshToken string `json:"refresh_token"`
			User         struct {
				ID          string         `json:"id"`
				AppMetadata map[string]any `json:"app_metadata"`
			} `json:"user"`
		}
		if err := json.Unmarshal(raw, &session); err != nil {
			t.Fatalf("failed to decode the session %q: %v", raw, err)
		}

		if session.AccessToken == "" || session.RefreshToken == "" || session.TokenType != "bearer" ||
			session.ExpiresIn == 0 || session.ExpiresAt == 0 || session.User.ID != userID {
			t.Errorf("incomplete session for %s: %s", userID, raw)
		}

		if got := session.User.AppMetadata["identity_source"]; got != "ldap" {
			t.Errorf("app_metadata.identity_source = %v, want ldap", got)
		}

		if _, err := issuer.VerifyMagicLink(ctx, link.HashedToken); err == nil {
			t.Error("a magic link verified twice")
		}
	})

	t.Run("magic link for an unknown email signs up a new user", func(t *testing.T) {
		link, err := issuer.GenerateMagicLink(ctx, newExternalID()+"@ldap.neutree.local")
		if err != nil {
			t.Fatalf("generate_link failed: %v", err)
		}

		if link.UserID == "" {
			t.Fatal("generate_link returned no user")
		}

		deleteGoTrueUser(t, link.UserID)
	})
}
