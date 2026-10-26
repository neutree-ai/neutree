package dbtest

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// These tests cover the name of a user profile: nobody can change it, not
// even a user admin or a direct database write.

func userProfileName(t *testing.T, id string) string {
	t.Helper()

	var name string
	if err := GetTestDB(t).QueryRowContext(context.Background(),
		`SELECT (metadata).name FROM api.user_profiles WHERE id = $1`, id).Scan(&name); err != nil {
		t.Fatalf("no user profile for %s: %v", id, err)
	}

	return name
}

func seededAdminProfileID(t *testing.T) string {
	t.Helper()

	var id string
	if err := GetTestDB(t).QueryRowContext(context.Background(),
		`SELECT id FROM api.user_profiles WHERE (metadata).name = 'admin'`).Scan(&id); err != nil {
		t.Fatalf("no admin profile: %v", err)
	}

	return id
}

func renameProfile(t *testing.T, token, id, name string) (int, string) {
	t.Helper()

	metadata := profileMetadata(t, token, id)
	metadata["name"] = name

	return patchProfile(t, token, id, map[string]any{"metadata": metadata})
}

func TestUserProfileName_Immutable(t *testing.T) {
	admin := adminToken(t)

	t.Run("admin cannot rename another user", func(t *testing.T) {
		user := createLocalEmailUser(t, "name-other")
		before := userProfileName(t, user.id)

		code, raw := renameProfile(t, admin, user.id, before+"-renamed")
		if code != http.StatusBadRequest || !strings.Contains(raw, `"code":"10265"`) {
			t.Fatalf("rename = HTTP %d, want 400 with code 10265: %s", code, raw)
		}

		if got := userProfileName(t, user.id); got != before {
			t.Errorf("name = %q, want it unchanged %q", got, before)
		}
	})

	t.Run("a user admin cannot rename admin", func(t *testing.T) {
		userAdmin := userTokenWithPermissions(t, "name-useradmin", []string{"user_profile:read", "user_profile:update", "user_profile:delete"})
		adminID := seededAdminProfileID(t)

		code, raw := renameProfile(t, userAdmin, adminID, "not-admin")
		if code != http.StatusBadRequest || !strings.Contains(raw, `"code":"10265"`) {
			t.Fatalf("rename admin = HTTP %d, want 400 with code 10265: %s", code, raw)
		}

		if got := userProfileName(t, adminID); got != "admin" {
			t.Errorf("admin name = %q, want admin", got)
		}
	})

	t.Run("other fields can still change", func(t *testing.T) {
		user := createLocalEmailUser(t, "name-display")
		before := userProfileName(t, user.id)

		metadata := profileMetadata(t, admin, user.id)
		metadata["display_name"] = "Changed by admin"

		code, raw := patchProfile(t, admin, user.id, map[string]any{"metadata": metadata})
		if code != http.StatusOK {
			t.Fatalf("display name PATCH = HTTP %d, want 200: %s", code, raw)
		}

		if got := userProfileName(t, user.id); got != before {
			t.Errorf("name = %q, want it unchanged %q", got, before)
		}
	})

	t.Run("a direct database write cannot rename", func(t *testing.T) {
		user := createLocalEmailUser(t, "name-sql")

		_, err := GetTestDB(t).ExecContext(context.Background(), `
			UPDATE api.user_profiles
			SET metadata = ROW('renamed-by-sql', (metadata).display_name, (metadata).workspace,
			                   (metadata).deletion_timestamp, (metadata).creation_timestamp,
			                   (metadata).update_timestamp, (metadata).labels, (metadata).annotations)::api.metadata
			WHERE id = $1`, user.id)
		if err == nil || !strings.Contains(err.Error(), "10265") {
			t.Fatalf("SQL rename error = %v, want code 10265", err)
		}
	})
}
