package dbtest

import (
	"net/http"
	"testing"
)

// api.session_is_active tells neutree-api whether the session of a user token
// still stands.
func TestSessionIsActive(t *testing.T) {
	user := createGoTrueTestUser(t, "session-check", "session-check-pw-1")
	gotruePasswordLogin(t, user.Email, "session-check-pw-1")

	db := GetTestDB(t)
	st := NewTestStorage(t)

	var sessionID string
	if err := db.QueryRow(`SELECT id FROM auth.sessions WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`, user.ID).Scan(&sessionID); err != nil {
		t.Fatalf("no session after login: %v", err)
	}

	check := func(t *testing.T, sessionID, userID string, want bool) {
		t.Helper()

		got, err := st.IsSessionActive(sessionID, userID)
		if err != nil || got != want {
			t.Errorf("IsSessionActive(%s, %s) = %v, %v; want %v", sessionID, userID, got, err, want)
		}
	}

	t.Run("a live session is active", func(t *testing.T) {
		check(t, sessionID, user.ID, true)
	})

	t.Run("the session of another user is not", func(t *testing.T) {
		check(t, sessionID, "00000000-0000-0000-0000-000000000000", false)
	})

	t.Run("a banned user's session is not", func(t *testing.T) {
		if _, err := db.Exec(`UPDATE auth.users SET banned_until = now() + interval '1 hour' WHERE id = $1`, user.ID); err != nil {
			t.Fatalf("ban: %v", err)
		}

		check(t, sessionID, user.ID, false)

		if _, err := db.Exec(`UPDATE auth.users SET banned_until = now() - interval '1 second' WHERE id = $1`, user.ID); err != nil {
			t.Fatalf("lift the ban: %v", err)
		}

		check(t, sessionID, user.ID, true)
	})

	t.Run("a session past not_after is not", func(t *testing.T) {
		if _, err := db.Exec(`UPDATE auth.sessions SET not_after = now() - interval '1 second' WHERE id = $1`, sessionID); err != nil {
			t.Fatalf("expire: %v", err)
		}

		check(t, sessionID, user.ID, false)

		if _, err := db.Exec(`UPDATE auth.sessions SET not_after = NULL WHERE id = $1`, sessionID); err != nil {
			t.Fatalf("restore: %v", err)
		}
	})

	t.Run("a deleted session is not", func(t *testing.T) {
		if _, err := db.Exec(`DELETE FROM auth.sessions WHERE id = $1`, sessionID); err != nil {
			t.Fatalf("delete: %v", err)
		}

		check(t, sessionID, user.ID, false)
	})

	t.Run("only service_role may call it", func(t *testing.T) {
		body := mustJSON(map[string]any{"p_session_id": sessionID, "p_user_id": user.ID})

		if code, raw := postgrestAs(t, adminToken(t), http.MethodPost, "/rpc/session_is_active", body); code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("api_user call = HTTP %d, want 401 or 403: %s", code, raw)
		}

		if code, raw := postgrestAs(t, "", http.MethodPost, "/rpc/session_is_active", body); code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("anonymous call = HTTP %d, want 401 or 403: %s", code, raw)
		}
	})
}
